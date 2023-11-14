package main

import (
	"io"
	"os"
	"fmt"
	"log"
	"net"
	"time"
	"flag"
	"sync"
	"bytes"
	"strconv"
	"strings"
	"os/exec"
	"net/http"
	"math/rand"
	"crypto/md5"
	"sync/atomic"
	"path/filepath"
	"encoding/binary"
)

var commandsName = map[int]string{
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
	id int32
	data string
}

type REQ struct {
	RandID int64
	GuestUUID[16] byte
	GuestID int64
	IsReq int32
	IsResp int32
	NeedResponse int32
	ReqLength int32
	RespLength int32
	CommandID int32
	SubCommand int32
	Reserve int32
}

const Header = 64
const Packet = 4096

var Chan chan RET
var WaitingFor int32
var Writer sync.Mutex
var Connection net.Conn
var Executed atomic.Bool

var GuestCPUs = flag.Int("cpu", 1, "Number of CPU cores")
var VmVersion = flag.String("version", "2.6.5-12202", "VM Version")
var VmTimestamp = flag.Int("ts", int(time.Now().Unix()), "VM Time")
var HostFixNumber = flag.Int("fixNumber", 0, "Fix number of Host")
var HostBuildNumber = flag.Int("build", 69057, "Build number of Host")
var HostModel = flag.String("model", "Virtualhost", "Host model name")
var HostMAC = flag.String("mac", "00:00:00:00:00:00", "Host MAC address")
var HostSN = flag.String("hostsn", "0000000000000", "Host serial number")
var GuestSN = flag.String("guestsn", "0000000000000", "Guest serial number")
var GuestCPU_ARCH = flag.String("cpu_arch", "QEMU, Virtual CPU, X86_64", "CPU arch")

var ApiPort = flag.String("api", ":2210", "API port")
var ListenAddr = flag.String("addr", "0.0.0.0:12345", "Listen address")

func main() {

	flag.Parse()

	go http_listener(*ApiPort)

	listener, err := net.Listen("tcp", *ListenAddr)

	if err != nil {
		log.Println("Error listening:", err)
		return
	}

	defer listener.Close()

	fmt.Println("Start listen on " + *ListenAddr)

	for {
		conn, err := listener.Accept()

		if err != nil {
			log.Println("Error on accept:", err)
			return
		}

		fmt.Printf("New connection from %s\n", conn.RemoteAddr().String())

		go incoming_conn(conn)
	}
}

func http_listener(port string) {

	Chan = make(chan RET, 1)

	router := http.NewServeMux()
	router.HandleFunc("/", home)
	router.HandleFunc("/read", read)
	router.HandleFunc("/write", write)

	err := http.ListenAndServe(port, router)

	if err != nil && err != http.ErrServerClosed {
		log.Fatalf("Error listening: %s", err)
	}
}

func incoming_conn(conn net.Conn) {

	defer conn.Close()
	Connection = conn

	for {
		buf := make([]byte, Packet)
		len, err := conn.Read(buf)

		if err != nil {
			if err != io.EOF {
				log.Println("Read error:", err)
			} else {
				fmt.Println("Disconnected:", err)
			}
			if len != Packet { return }
		}

		if len != Packet {
			// Something wrong, close and wait for reconnect
			log.Printf("Read error: Received %d bytes, not %d\n", len, Packet)
			return
		}

		process_req(buf, conn)
	}
}

func process_req(buf []byte, conn net.Conn) {

	var req REQ

	err := binary.Read(bytes.NewReader(buf), binary.LittleEndian, &req)

	if err != nil {
		log.Printf("Error on decode: %s\n", err)
		return
	}

	var data string
	var title string

	if req.IsReq == 1 {

		title = "Received"
		data = string(buf[Header : Header+req.ReqLength])
		if req.CommandID == 3 { Executed.Store(false) }

	} else if req.IsResp == 1 {

		title = "Response"
		data = string(buf[Header : Header+req.RespLength])

		if req.CommandID == atomic.LoadInt32(&WaitingFor) && req.CommandID != 0 {
			atomic.StoreInt32(&WaitingFor, 0)
			resp := RET{
				id: req.CommandID,
				data: strings.Replace(data, "\x00", "", -1),
			}
			Chan <- resp
		}
	}

	fmt.Printf("%s: %s [%d] %s \n", title, commandsName[int(req.CommandID)],
		int(req.CommandID), strings.Replace(data, "\x00", "", -1))

	// if it's a req and need a response
	if req.IsReq == 1 && req.NeedResponse == 1 {
		process_resp(req, conn)
	}
}

func process_resp(req REQ, conn net.Conn) {

	req.IsReq = 0
	req.IsResp = 1
	req.ReqLength = 0
	req.RespLength = 0
	req.NeedResponse = 0

	data := payload(req)

	if data != "" {
		req.RespLength = int32(len([]byte(data)) + 1)
	} else if req.CommandID != 10 {
		log.Printf("No handler available for command: %d\n", req.CommandID)
	}

	fmt.Printf("Replied: %s [%d] \n", data, int(req.CommandID))

	logerr(conn.Write(packet(req, data)))
}

func packet(req REQ, data string) []byte {

	buf := make([]byte, 0, Packet)
	writer := bytes.NewBuffer(buf)

	// write to buf
	logw(binary.Write(writer, binary.LittleEndian, &req))
	if data != "" { writer.Write([]byte(data)) }

	// full fill 4096
	buf = make([]byte, Packet)
	copy(buf, writer.Bytes())

	return buf
}

func payload(req REQ) string {

	var data string

	switch req.CommandID {
		case 4: // Host version
			data = fmt.Sprintf(`{"buildnumber":%d,"smallfixnumber":%d}`,
				*HostBuildNumber, *HostFixNumber)
		case 5: // Guest SN
			data = strings.ToUpper(*GuestSN)
		case 7: // CPU info
			data = fmt.Sprintf(`{"cpuinfo":"%s","vcpu_num":%d}`,
				*GuestCPU_ARCH+", "+strconv.Itoa(*GuestCPUs), *GuestCPUs)
		case 8: // VM version
			data = fmt.Sprintf(`{"id":"Virtualization","name":"Virtual Machine Manager","timestamp":%d,"version":"%s"}`,
				*VmTimestamp, *VmVersion)
		case 11: // Guest UUID
			run_once()
			data = uuid(guest_id())
		case 12: // Cluster UUID
			run_once()
			data = uuid(host_id())
		case 13: // Host SN
			data = strings.ToUpper(*HostSN)
		case 14: // Host MAC
			data = strings.ToLower(strings.ReplaceAll(*HostMAC, "-", ":"))
		case 15: // Host model
			data = *HostModel
		case 16: // Update Dead line time, always 0x7fffffffffffffff
			data = "9223372036854775807"
	}

	return data
}

func send_command(CommandID int32, SubCommand int32, needsResp int32) bool {

	req := REQ{
		IsReq: 1,
		IsResp: 0,
		ReqLength: 0,
		RespLength: 0,
		GuestID: 10000000,
		RandID: rand.Int63(),
		GuestUUID: guest_id(),
		NeedResponse: needsResp,
		CommandID: CommandID,
		SubCommand: SubCommand,
	}

	//fmt.Printf("Writing command %d\n", CommandID)

	if Connection == nil { return false }
	_, err := Connection.Write(packet(req, ""))
	if err == nil { return true }

	log.Println("Write error:", err)
	return false
}

func read(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	Writer.Lock()
	defer Writer.Unlock()

	query := r.URL.Query()
	commandID, err := strconv.Atoi(query.Get("command"))

	if err != nil || commandID < 1 {
		fail(w, fmt.Sprintf("Failed to parse command %s \n", query.Get("command")))
		return
	}

	if Connection == nil || Chan == nil {
		fail(w, "No connection to guest")
		return
	}

	for len(Chan) > 0 {
		log.Printf("Warning: channel was not empty?")
		<-Chan
	}

	fmt.Printf("Request: %s [%d] \n", commandsName[commandID], commandID)
	atomic.StoreInt32(&WaitingFor, (int32)(commandID))

	if !send_command((int32)(commandID), 1, 1) {
		atomic.StoreInt32(&WaitingFor, 0)
		fail(w, fmt.Sprintf("Failed reading command %d from guest \n", commandID))
		return
	}

	var resp RET

	select {
		case res := <-Chan:
			resp = res
		case <-time.After(15 * time.Second):
			atomic.StoreInt32(&WaitingFor, 0)
			fail(w, fmt.Sprintf("Timeout while reading command %d from guest \n", commandID))
			return
	}

	atomic.StoreInt32(&WaitingFor, 0)

	if resp.id != (int32)(commandID) {
		fail(w, fmt.Sprintf("Received wrong response for command %d from guest: %d \n", commandID, resp.id))
		return
	}

	if resp.data == "" && resp.id != 6 {
		fail(w, fmt.Sprintf("Received no data for command %d \n", commandID))
		return
	}

	ok(w, resp.data)
}

func write(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	Writer.Lock()
	defer Writer.Unlock()

	if Connection == nil {
		fail(w, "No connection to guest")
		return
	}

	query := r.URL.Query()
	commandID, err := strconv.Atoi(query.Get("command"))

	if err != nil || commandID < 1 {
		fail(w, fmt.Sprintf("Failed to parse command %s \n", query.Get("command")))
		return
	}

	fmt.Printf("Command: %s [%d] \n", commandsName[commandID], commandID)

	if !send_command((int32)(commandID), 1, 0) {
		fail(w, fmt.Sprintf("Failed sending command %d to guest \n", commandID))
		return
	}

	ok(w, "")
}

func home(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")
	fail(w, "No command specified")
}

func fail(w http.ResponseWriter, msg string) {

	log.Printf("API: " + msg)
	msg = strings.Replace(msg, "\"", "", -1)
	w.WriteHeader(http.StatusInternalServerError)
	logerr(w.Write([]byte(`{"status": "error", "data": null, "message": "` + msg + `"}`)))
}

func ok(w http.ResponseWriter, data string) {

	if data == "" { data = "null" }
	w.WriteHeader(http.StatusOK)
	logerr(w.Write([]byte(`{"status": "success", "data": ` + data + `, "message": null}`)))
}

func logerr(n int, err error) {
	logw(err)
}

func logw(err error) {
	if err != nil { log.Println("Write failed:", err) }
}

func host_id() [16]byte {
	return md5.Sum([]byte("h" + strings.ToUpper(*HostSN)))
}

func guest_id() [16]byte {
	return md5.Sum([]byte("g" + strings.ToUpper(*GuestSN)))
}

func uuid(b [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%12x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func run_once() {

	if Executed.Load() { return }

	Executed.Store(true)
	file := path() + "/print.sh"
	if exists(file) { execute(file, nil) }
}

func path() string {

	exePath, err := os.Executable()
	if err == nil { return filepath.Dir(exePath) }

	log.Println("Path error:", err)
	return ""
}

func exists(name string) bool {

	_, err := os.Stat(name)
	return err == nil
}

func execute(script string, command []string) bool {

	cmd := &exec.Cmd{
		Path:   script,
		Args:   command,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	err := cmd.Start()
	if err == nil { return true }

	log.Println("Cannot run:", err)
	return false
}
