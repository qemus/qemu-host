package main

import (
	"io"
	"os"
	"fmt"
	"log"
	"net"
	"flag"
	"sync"
	"time"
	"bytes"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"net/http"
	"math/rand"
	"crypto/md5"
	"sync/atomic"
	"encoding/json"
	"path/filepath"
	"encoding/binary"
)

var commandsName = map[int32]string{
	2:  "Guest info",
	3:  "Guest power",
	4:  "Host version",
	5:  "Guest SN",
	6:  "Guest shutdown",
	7:  "Guest CPU info",
	8:  "VM version",
	9:  "Host version",
	10: "Get Guest Info",
	11: "Guest UUID",
	12: "Cluster UUID",
	13: "Host SN",
	14: "Host MAC",
	15: "Host model",
	16: "Update Deadline",
	17: "Guest Timestamp",
}

type RET struct {
	req  REQ
	data string
}

type REQ struct {
	RandID       int64
	GuestUUID    [16]byte
	GuestID      int64
	IsReq        int32
	IsResp       int32
	NeedResponse int32
	ReqLength    int32
	RespLength   int32
	CommandID    int32
	SubCommand   int32
	Reserve      int32
}

type connectionState struct {
	conn    net.Conn
	writeMu sync.Mutex
}

type pendingResult struct {
	response RET
	err      error
}

type pendingRequest struct {
	connection *connectionState
	commandID  int32
	result     chan pendingResult
}

type apiResponse struct {
	Status  string `json:"status"`
	Data    any    `json:"data"`
	Message any    `json:"message"`
}

const (
	HeaderSize        = 64
	PacketSize        = 4096
	maxPayloadSize    = PacketSize - HeaderSize - 1
	maxTimeoutSeconds = int64((1<<63 - 1) / int64(time.Second))
)

var (
	Version string

	writerMu sync.Mutex

	connectionMu     sync.RWMutex
	activeConnection *connectionState

	pendingMu sync.Mutex
	pending   *pendingRequest

	executed atomic.Bool
)

var (
	GuestCPUs       = flag.Int("cpu", 1, "Number of CPU cores")
	VMVersion       = flag.String("version", "2.6.5-12202", "VM Version")
	VMTimestamp     = flag.Int("ts", int(time.Now().Unix()), "VM Time")
	HostFixNumber   = flag.Int("fixNumber", 0, "Fix number of Host")
	HostBuildNumber = flag.Int("build", 69057, "Build number of Host")
	HostModel    = flag.String("model", "Virtualhost", "Host model name")
	HostMAC      = flag.String("mac", "00:00:00:00:00:00", "Host MAC address")
	HostSN       = flag.String("hostsn", "0000000000000", "Host serial number")
	GuestSN      = flag.String("guestsn", "0000000000000", "Guest serial number")
	GuestCPUArch = flag.String("cpu_arch", "QEMU, Virtual CPU, X86_64", "CPU arch")

	APIPort    = flag.String("api", ":2210", "API TCP address or Unix socket path")
	APITimeout = flag.Int("timeout", 10, "Default timeout")
	ListenAddr = flag.String("addr", "0.0.0.0:12345", "Guest TCP address or Unix socket path")
)

func init() {
	if size := binary.Size(REQ{}); size != HeaderSize {
		panic(fmt.Sprintf("REQ header size is %d bytes, expected %d", size, HeaderSize))
	}
}

func main() {
	flag.Parse()
	validateOptions()

	listener, err := openListener(*ListenAddr)
	if err != nil {
		log.Println("Error starting guest listener:", err)
		return
	}
	defer func() { _ = listener.Close() }()

	apiListener, err := openListener(*APIPort)
	if err != nil {
		log.Println("Error starting API listener:", err)
		return
	}
	defer func() { _ = apiListener.Close() }()

	go httpListener(apiListener)

	fmt.Printf("Version %s started listening on %s\n", Version, listener.Addr())

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Error on accept:", err)
			return
		}

		fmt.Printf("New connection from %s\n", conn.RemoteAddr())
		go incomingConn(conn)
	}
}

func parseListener(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", errors.New("listener address cannot be empty")
	}

	switch {
	case strings.HasPrefix(value, "unix:"):
		address := strings.TrimPrefix(value, "unix:")
		if !filepath.IsAbs(address) {
			return "", "", errors.New("Unix socket path must be absolute")
		}
		return "unix", address, nil

	case strings.HasPrefix(value, "tcp:"):
		address := strings.TrimPrefix(value, "tcp:")
		if address == "" {
			return "", "", errors.New("TCP listener address cannot be empty")
		}
		return "tcp", address, nil

	case filepath.IsAbs(value):
		return "unix", value, nil

	default:
		return "tcp", value, nil
	}
}

func removeSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s already exists and is not a Unix socket", path)
	}

	return os.Remove(path)
}

func openListener(value string) (net.Listener, error) {
	network, address, err := parseListener(value)
	if err != nil {
		return nil, err
	}

	if network == "unix" {
		if err := removeSocket(address); err != nil {
			return nil, fmt.Errorf("prepare Unix socket: %w", err)
		}
	}

	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}

	if unixListener, ok := listener.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(true)
	}

	return listener, nil
}

func validateOptions() {
	if *GuestCPUs < 1 {
		log.Fatal("CPU count must be at least 1")
	}
	if *APITimeout < 1 || int64(*APITimeout) > maxTimeoutSeconds {
		log.Fatalf("Default timeout must be between 1 and %d seconds", maxTimeoutSeconds)
	}
}

func httpListener(listener net.Listener) {
	router := http.NewServeMux()
	router.HandleFunc("/", home)
	router.HandleFunc("/read", read)
	router.HandleFunc("/write", write)

	err := http.Serve(listener, router)
	if err != nil &&
		!errors.Is(err, http.ErrServerClosed) &&
		!errors.Is(err, net.ErrClosed) {
		log.Fatalf("Error listening: %s", err)
	}
}

func incomingConn(conn net.Conn) {
	state := &connectionState{conn: conn}
	setConnection(state)

	defer func() {
		clearConnection(state)
		_ = conn.Close()
	}()

	buf := make([]byte, PacketSize)

	for {
		_, err := io.ReadFull(conn, buf)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF ||
				errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed) {
				fmt.Println("Disconnected:", err)
			} else {
				log.Println("Read error:", err)
			}
			return
		}

		processReq(buf, state)
	}
}

func setConnection(state *connectionState) {
	connectionMu.Lock()
	old := activeConnection
	activeConnection = state
	connectionMu.Unlock()

	if old != nil && old != state {
		failPending(old, errors.New("guest connection was replaced"))
		_ = old.conn.Close()
	}
}

func clearConnection(state *connectionState) {
	connectionMu.Lock()
	if activeConnection == state {
		activeConnection = nil
	}
	connectionMu.Unlock()

	failPending(state, errors.New("guest disconnected"))
}

func getConnection() *connectionState {
	connectionMu.RLock()
	state := activeConnection
	connectionMu.RUnlock()

	return state
}

func processReq(buf []byte, state *connectionState) {
	var req REQ

	if err := binary.Read(bytes.NewReader(buf[:HeaderSize]), binary.LittleEndian, &req); err != nil {
		log.Printf("Error on decode: %s\n", err)
		return
	}

	if req.NeedResponse != 0 && req.NeedResponse != 1 {
		log.Printf("Invalid NeedResponse value: %d\n", req.NeedResponse)
		return
	}

	var (
		data  string
		title string
		err   error
	)

	switch {
	case req.IsReq == 1 && req.IsResp == 0:
		title = "Received"
		data, err = packetData(buf, req.ReqLength)
		if req.CommandID == 3 {
			executed.Store(false)
		}

	case req.IsReq == 0 && req.IsResp == 1:
		title = "Response"
		data, err = packetData(buf, req.RespLength)

	default:
		log.Printf("Invalid packet flags: IsReq=%d IsResp=%d\n", req.IsReq, req.IsResp)
		return
	}

	if err != nil {
		log.Printf("Invalid packet for command %d: %s\n", req.CommandID, err)
		return
	}

	cleanData := strings.TrimRight(data, "\x00")
	fmt.Printf("%s: %s [%d] %s\n", title, commandName(req.CommandID), req.CommandID, cleanData)

	if req.IsResp == 1 {
		deliverResponse(state, req, cleanData)
		return
	}

	if req.NeedResponse == 1 {
		processResp(req, state)
	}
}

func packetData(buf []byte, length int32) (string, error) {
	if length < 0 {
		return "", fmt.Errorf("negative payload length %d", length)
	}
	if length > int32(PacketSize-HeaderSize) {
		return "", fmt.Errorf("payload length %d exceeds maximum %d", length, PacketSize-HeaderSize)
	}

	end := HeaderSize + int(length)
	if end > len(buf) {
		return "", fmt.Errorf("payload ends at byte %d, packet has %d bytes", end, len(buf))
	}

	return string(buf[HeaderSize:end]), nil
}

func processResp(req REQ, state *connectionState) {
	req.IsReq = 0
	req.IsResp = 1
	req.ReqLength = 0
	req.RespLength = 0
	req.NeedResponse = 0

	data, handled, err := payload(req)
	if err != nil {
		log.Printf("Failed creating response for command %d: %s\n", req.CommandID, err)
		return
	}

	if handled {
		if len(data) > maxPayloadSize {
			log.Printf(
				"Response for command %d is %d bytes, maximum is %d\n",
				req.CommandID,
				len(data),
				maxPayloadSize,
			)
			return
		}
		req.RespLength = int32(len(data) + 1)
	} else {
		log.Printf("No handler available for command: %d\n", req.CommandID)
	}

	fmt.Printf("Replied: %s [%d]\n", data, req.CommandID)

	if err := sendPacket(state, req, data); err != nil {
		log.Println("Write failed:", err)
	}
}

func encodePacket(req REQ, data string) ([]byte, error) {
	if len(data) > maxPayloadSize {
		return nil, fmt.Errorf("payload is %d bytes, maximum is %d", len(data), maxPayloadSize)
	}

	writer := bytes.NewBuffer(make([]byte, 0, HeaderSize))
	if err := binary.Write(writer, binary.LittleEndian, &req); err != nil {
		return nil, fmt.Errorf("encode header: %w", err)
	}
	if writer.Len() != HeaderSize {
		return nil, fmt.Errorf("encoded header is %d bytes, expected %d", writer.Len(), HeaderSize)
	}

	buf := make([]byte, PacketSize)
	copy(buf, writer.Bytes())
	copy(buf[HeaderSize:], data)

	return buf, nil
}

func sendPacket(state *connectionState, req REQ, data string) error {
	buf, err := encodePacket(req, data)
	if err != nil {
		return err
	}

	return state.writePacket(buf)
}

func (state *connectionState) writePacket(buf []byte) error {
	state.writeMu.Lock()
	defer state.writeMu.Unlock()

	for len(buf) > 0 {
		n, err := state.conn.Write(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		buf = buf[n:]
	}

	return nil
}

func payload(req REQ) (string, bool, error) {
	switch req.CommandID {
	case 4: // Host version
		return marshalPayload(struct {
			BuildNumber    int `json:"buildnumber"`
			SmallFixNumber int `json:"smallfixnumber"`
		}{
			BuildNumber:    *HostBuildNumber,
			SmallFixNumber: *HostFixNumber,
		})

	case 5: // Guest SN
		runOnce()
		return strings.ToUpper(*GuestSN), true, nil

	case 7: // CPU info
		return marshalPayload(struct {
			CPUInfo string `json:"cpuinfo"`
			VCPUNum int    `json:"vcpu_num"`
		}{
			CPUInfo: *GuestCPUArch + ", " + strconv.Itoa(*GuestCPUs),
			VCPUNum: *GuestCPUs,
		})

	case 8: // VM version
		return marshalPayload(struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Timestamp int    `json:"timestamp"`
			Version   string `json:"version"`
		}{
			ID:        "Virtualization",
			Name:      "Virtual Machine Manager",
			Timestamp: *VMTimestamp,
			Version:   *VMVersion,
		})

	case 10: // Set network
		return marshalPayload(struct {
			Detail []struct {
				Success bool   `json:"success"`
				Type    string `json:"type"`
			} `json:"detail"`
		}{
			Detail: []struct {
				Success bool   `json:"success"`
				Type    string `json:"type"`
			}{
				{Success: false, Type: "set_net"},
			},
		})

	case 11: // Guest UUID
		runOnce()
		return uuid(guestID()), true, nil

	case 12: // Cluster UUID
		runOnce()
		return uuid(hostID()), true, nil

	case 13: // Host SN
		runOnce()
		return strings.ToUpper(*HostSN), true, nil

	case 14: // Host MAC
		return strings.ToLower(strings.ReplaceAll(*HostMAC, "-", ":")), true, nil

	case 15: // Host model
		return *HostModel, true, nil

	case 16: // Update deadline, always 0x7fffffffffffffff
		return "9223372036854775807", true, nil

	default:
		return "", false, nil
	}
}

func marshalPayload(value any) (string, bool, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", false, err
	}

	return string(data), true, nil
}

func newRequest(commandID int32, subCommand int32, needsResponse int32) REQ {
	return REQ{
		RandID:       rand.Int63(),
		GuestUUID:    guestID(),
		GuestID:      10000000,
		IsReq:        1,
		IsResp:       0,
		NeedResponse: needsResponse,
		ReqLength:    0,
		RespLength:   0,
		CommandID:    commandID,
		SubCommand:   subCommand,
	}
}

func registerPending(request *pendingRequest) bool {
	pendingMu.Lock()
	defer pendingMu.Unlock()

	if pending != nil {
		return false
	}

	pending = request
	return true
}

func cancelPending(request *pendingRequest) bool {
	pendingMu.Lock()
	defer pendingMu.Unlock()

	if pending != request {
		return false
	}

	pending = nil
	return true
}

func deliverResponse(state *connectionState, req REQ, data string) {
	pendingMu.Lock()
	request := pending
	if request == nil || request.connection != state ||
		req.CommandID == 0 || req.CommandID != request.commandID {
		pendingMu.Unlock()
		return
	}
	pending = nil
	pendingMu.Unlock()

	// Preserve the original protocol behavior: responses are matched only by
	// CommandID. RandID is not assumed to be echoed by the guest.
	request.result <- pendingResult{
		response: RET{
			req:  req,
			data: data,
		},
	}
}

func failPending(state *connectionState, err error) {
	pendingMu.Lock()
	request := pending
	if request == nil || request.connection != state {
		pendingMu.Unlock()
		return
	}
	pending = nil
	pendingMu.Unlock()

	request.result <- pendingResult{err: err}
}

func read(w http.ResponseWriter, r *http.Request) {
	writerMu.Lock()
	defer writerMu.Unlock()

	commandID, err := parseCommand(r.URL.Query().Get("command"))
	if err != nil {
		fail(w, err.Error())
		return
	}

	wait, err := parseTimeout(r.URL.Query().Get("timeout"))
	if err != nil {
		fail(w, err.Error())
		return
	}

	state := getConnection()
	if state == nil {
		fail(w, "No connection to guest")
		return
	}

	req := newRequest(commandID, 1, 1)
	request := &pendingRequest{
		connection: state,
		commandID:  commandID,
		result:     make(chan pendingResult, 1),
	}

	if !registerPending(request) {
		fail(w, "A previous request is still pending")
		return
	}

	fmt.Printf("Request: %s [%d]\n", commandName(commandID), commandID)

	if err := sendPacket(state, req, ""); err != nil {
		cancelPending(request)
		fail(w, fmt.Sprintf("Failed reading command %d from guest: %s", commandID, err))
		return
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()

	var result pendingResult

	select {
	case result = <-request.result:
	case <-timer.C:
		if cancelPending(request) {
			fail(w, fmt.Sprintf("Timeout while reading command %d from guest", commandID))
			return
		}

		// A response or disconnect claimed the request at the same moment the
		// timer fired. Wait for the already-committed result.
		result = <-request.result
	}

	if result.err != nil {
		fail(w, fmt.Sprintf("Failed reading command %d from guest: %s", commandID, result.err))
		return
	}

	resp := result.response
	if resp.data == "" && commandID != 6 {
		fail(w, fmt.Sprintf("Received no data for command %d", commandID))
		return
	}

	ok(w, resp.data)
}

func write(w http.ResponseWriter, r *http.Request) {
	writerMu.Lock()
	defer writerMu.Unlock()

	commandID, err := parseCommand(r.URL.Query().Get("command"))
	if err != nil {
		fail(w, err.Error())
		return
	}

	state := getConnection()
	if state == nil {
		fail(w, "No connection to guest")
		return
	}

	fmt.Printf("Command: %s [%d]\n", commandName(commandID), commandID)

	req := newRequest(commandID, 1, 0)
	if err := sendPacket(state, req, ""); err != nil {
		fail(w, fmt.Sprintf("Failed sending command %d to guest: %s", commandID, err))
		return
	}

	ok(w, "")
}

func parseCommand(value string) (int32, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("no command specified")
	}

	commandID, err := strconv.ParseInt(value, 10, 32)
	if err != nil || commandID < 1 {
		return 0, fmt.Errorf("failed to parse command: %s", value)
	}

	return int32(commandID), nil
}

func parseTimeout(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Duration(*APITimeout) * time.Second, nil
	}

	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < 1 || seconds > maxTimeoutSeconds {
		return 0, fmt.Errorf("failed to parse timeout: %s", value)
	}

	return time.Duration(seconds) * time.Second, nil
}

func commandName(commandID int32) string {
	if name, ok := commandsName[commandID]; ok {
		return name
	}

	return "Unknown command"
}

func home(w http.ResponseWriter, _ *http.Request) {
	fail(w, "No command specified")
}

func fail(w http.ResponseWriter, msg string) {
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}

	log.Println("API: " + msg)
	writeAPIResponse(w, http.StatusInternalServerError, apiResponse{
		Status:  "error",
		Data:    nil,
		Message: msg,
	})
}

func ok(w http.ResponseWriter, data string) {
	writeAPIResponse(w, http.StatusOK, apiResponse{
		Status:  "success",
		Data:    apiData(data),
		Message: nil,
	})
}

func apiData(data string) any {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return nil
	}

	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) &&
		json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}

	return data
}

func writeAPIResponse(w http.ResponseWriter, status int, response apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Println("API write failed:", err)
	}
}

func hostID() [16]byte {
	return md5.Sum([]byte("h" + strings.ToUpper(*HostSN)))
}

func guestID() [16]byte {
	return md5.Sum([]byte("g" + strings.ToUpper(*GuestSN)))
}

func uuid(b [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%12x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func runOnce() {
	if !executed.CompareAndSwap(false, true) {
		return
	}

	dir, err := executableDir()
	if err != nil {
		executed.Store(false)
		log.Println("Path error:", err)
		return
	}

	file := filepath.Join(dir, "print.sh")
	if exists(file) && !execute(file, nil) {
		executed.Store(false)
	}
}

func executableDir() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}

	return filepath.Dir(exePath), nil
}

func exists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

func execute(script string, command []string) bool {
	cmd := exec.Command(script, command...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Println("Cannot run:", err)
		return false
	}

	go func() {
		if err := cmd.Wait(); err != nil {
			log.Println("Command failed:", err)
		}
	}()

	return true
}
