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

type RESP struct {
	id int32
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

var Chan chan RESP
var WaitingFor int32
var Writer sync.Mutex
var Connection net.Conn
var Executed atomic.Bool
var Shutdown atomic.Bool

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

	router := http.NewServeMux()
	router.HandleFunc("/", home)
	router.HandleFunc("/read", read)
	router.HandleFunc("/write", write)

	go http.ListenAndServe(*ApiPort, router)

	Chan = make(chan RESP, 1)
	listener, err := net.Listen("tcp", *ListenAddr)

	if err != nil {
		log.Println("Error listening:", err.Error())
		return
	}

	defer listener.Close()
	fmt.Println("Start listen on " + *ListenAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Error on accept:", err.Error())
			return
		} else {
			fmt.Printf("New connection from %s\n", conn.RemoteAddr().String())

			go incoming_conn(conn)
		}
	}
}

func incoming_conn(conn net.Conn) {

	defer conn.Close()
	Connection = conn

	for {
		buf := make([]byte, 4096)
		len, err := conn.Read(buf)

		if err != nil {
			if err != io.EOF && !Shutdown.Load() {
				log.Println("Read error:", err.Error())
			} else {
				fmt.Println("Disconnected:", err.Error())
			}
			if len != 4096 { return }
		}

		if len != 4096 {
			// Something wrong, close and wait for reconnect
			log.Printf("Read error: Received %d Bytes, not 4096\n", len)
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
		data = string(buf[64 : 64+req.ReqLength])
		if req.CommandID == 3 { Executed.Store(false) }

	} else if req.IsResp == 1 {

		title = "Response"
		data = string(buf[64 : 64+req.RespLength])
		if req.CommandID == 6 { Shutdown.Store(true) }

		if req.CommandID != 0 && req.CommandID == atomic.LoadInt32(&WaitingFor) {
			atomic.StoreInt32(&WaitingFor, 0)
			var resp RESP
			resp.id = req.CommandID
			resp.data = strings.Replace(data, "\x00", "", -1)
			Chan <- resp
		}
	}

	fmt.Printf("%s: %s [%d] %s \n", title, commandsName[int(req.CommandID)], int(req.CommandID), strings.Replace(data, "\x00", "", -1))

	// if it's a req and need a response
	if req.IsReq == 1 && req.NeedResponse == 1 {
		process_resp(req, conn)
	}
}

func process_resp(req REQ, conn net.Conn) bool {

	var data string

	switch req.CommandID {
	case 4:
		// Host version
		data = fmt.Sprintf(`{"buildnumber":%d,"smallfixnumber":%d}`, *HostBuildNumber, *HostFixNumber)
	case 5:
		// Guest SN
		data = *GuestSN
	case 7:
		// CPU info
		data = fmt.Sprintf(`{"cpuinfo":"%s","vcpu_num":%d}`,
			*GuestCPU_ARCH+", "+strconv.Itoa(*GuestCPUs), *GuestCPUs)
	case 8:
		// VM version
		data = fmt.Sprintf(`{"id":"Virtualization","name":"Virtual Machine Manager","timestamp":%d,"version":"%s"}`,
			*VmTimestamp, *VmVersion)
	case 11:
		run_once()
		// Guest UUID
		data = uuid(guest_id())
	case 12:
		run_once()
		// Cluster UUID
		data = uuid(host_id())
	case 13:
		// Host SN
		data = *HostSN
	case 14:
		// Host MAC
		data = strings.ToLower(strings.ReplaceAll(*HostMAC, "-", ":"))
	case 15:
		// Host model
		data = *HostModel
	case 16:
		// Update Dead line time, always 0x7fffffffffffffff
		data = "9223372036854775807"
	}

	if data == "" && req.CommandID != 10 {
		log.Printf("No handler available for command: %d\n", req.CommandID)
	}

	buf := make([]byte, 0, 4096)
	writer := bytes.NewBuffer(buf)

	req.IsReq = 0
	req.IsResp = 1
	req.ReqLength = 0
	req.NeedResponse = 0
	req.RespLength = int32(len([]byte(data)) + 1)

	fmt.Printf("Replied: %s [%d] \n", data, int(req.CommandID))

	// write to buf
	binary.Write(writer, binary.LittleEndian, &req)
	writer.Write([]byte(data))
	res := writer.Bytes()

	// full fill 4096
	buf = make([]byte, 4096, 4096)
	copy(buf, res)

	_, err := conn.Write(buf)
	if err == nil { return true }

	log.Println("Write error:", err.Error())
	return false
}

func fail(w http.ResponseWriter, msg string) {

	log.Printf("API: " + msg)
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte(`{"status": "error", "data": null, "message": "` + strings.Replace(msg, "\"", "", -1) + `"}`))
}

func ok(w http.ResponseWriter, data string) {

	if data == "" { data = "null" }
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "success", "data": ` + data + `, "message": null}`))
}

func home(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")
	fail(w, "No command specified")
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

	var resp RESP

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
	return
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
	return
}

func send_command(CommandID int32, SubCommand int32, needsResp int32) bool {

	var req REQ

	req.CommandID = CommandID
	req.SubCommand = SubCommand

	req.IsReq = 1
	req.IsResp = 0
	req.ReqLength = 0
	req.RespLength = 0
	req.GuestID = 10000000
	req.RandID = rand.Int63()
	req.GuestUUID = guest_id()
	req.NeedResponse = needsResp

	buf := make([]byte, 0, 4096)
	writer := bytes.NewBuffer(buf)

	// write to buf
	binary.Write(writer, binary.LittleEndian, &req)
	res := writer.Bytes()

	// full fill 4096
	buf = make([]byte, 4096, 4096)
	copy(buf, res)

	//fmt.Printf("Writing command %d\n", CommandID)

	if Connection == nil { return false }
	_, err := Connection.Write(buf)
	if err == nil { return true }

	log.Println("Write error:", err.Error())
	return false
}

func host_id() [16]byte {
	return md5.Sum([]byte("h" + *HostSN))
}

func guest_id() [16]byte {
	return md5.Sum([]byte("g" + *GuestSN))
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

	log.Println("Cannot run:", err.Error())
	return false
}
