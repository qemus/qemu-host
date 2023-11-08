package main

import (
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
	"sync/atomic"
	"path/filepath"
	"encoding/binary"
	mrand "math/rand"
        crand "crypto/rand"
)

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

type RESP struct {
	id   int32
	data string
}

var Chan chan RESP
var WaitingFor int32
var Writer sync.Mutex
var Reader sync.Mutex
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

var GuestUUID = flag.String("guestuuid", uuid(), "Guest UUID")
var ClusterUUID = flag.String("clusteruuid", uuid(), "Cluster UUID")

var ApiPort = flag.String("api", ":2210", "API port")
var ListenAddr = flag.String("addr", "0.0.0.0:12345", "Listen address")

func main() {

	flag.Parse()

	router := http.NewServeMux()
	router.HandleFunc("/", home)
	router.HandleFunc("/read", read)
	router.HandleFunc("/write", write)

	go http.ListenAndServe(*ApiPort, router)

	listener, err := net.Listen("tcp", *ListenAddr)

	if err != nil {
		log.Fatalln("Error listening:", err.Error())
		return
	}

	fmt.Println("Start listen on " + *ListenAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Error on accept:", err.Error())
		} else {
			fmt.Printf("New connection from %s\n", conn.RemoteAddr().String())

			go incoming_conn(conn)
		}
	}
}

func incoming_conn(conn net.Conn) {

	Connection = conn
	Executed.Store(false)
	Shutdown.Store(false)

	if Chan == nil { Chan = make(chan RESP, 1) }

	for {
		buf := make([]byte, 4096)
		len, err := conn.Read(buf)
		if err != nil {
			if !Shutdown.Load() {
				log.Println("Read error:", err.Error())
			}
			if len != 4096 { return }
		}
		if len != 4096 {
			log.Printf("Read error: Received %d Bytes, not 4096\n", len)
			// Something wrong, close and wait for reconnect
			conn.Close()
			return
		}
		go process_req(buf, conn)
		//fmt.Printf("Read %d Bytes\n%#v\n", len, string(buf[:len]))
	}
}

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

func process_req(buf []byte, conn net.Conn) {

	Reader.Lock()
	defer Reader.Unlock()

	var req REQ
	var data string

	err := binary.Read(bytes.NewReader(buf), binary.LittleEndian, &req)
	if err != nil {
		log.Printf("Error on decode: %s\n", err)
		return
	}

	if req.IsReq == 1 {
		data = string(buf[64 : 64+req.ReqLength])
	} else if req.IsResp == 1 {
		data = string(buf[64 : 64+req.RespLength])
	
		if req.CommandID == atomic.LoadInt32(&WaitingFor) {
			atomic.StoreInt32(&WaitingFor, 0)
			var resp RESP
			resp.id = req.CommandID
			resp.data = strings.Replace(data, "\x00", "", -1)
			Chan <- resp
		}
	}

	fmt.Printf("Command: %s [%d]\n", commandsName[int(req.CommandID)], int(req.CommandID))
	if data != "" { fmt.Printf("Info: %s\n", data) }

	// Hard code of command
	switch req.CommandID {
	case 2:
		// Guest Info
	case 3:
		// Guest start/reboot
	case 4:
		// Host version
		data = fmt.Sprintf(`{"buildnumber":%d,"smallfixnumber":%d}`, *HostBuildNumber, *HostFixNumber)
	case 5:
		// Guest SN
		data = *GuestSN
	case 6:
		// Guest shutdown
	case 7:
		// CPU info
		data = fmt.Sprintf(`{"cpuinfo":"%s","vcpu_num":%d}`,
			*GuestCPU_ARCH+", "+strconv.Itoa(*GuestCPUs), *GuestCPUs)
	case 8:
		data = fmt.Sprintf(`{"id":"Virtualization","name":"Virtual Machine Manager","timestamp":%d,"version":"%s"}`,
			*VmTimestamp, *VmVersion)
	case 9:
		// Version Info
	case 10:
		// Guest Info
	case 11:
		// Guest UUID
		data = *GuestUUID
		run_once()
	case 12:
		// Cluster UUID
		data = *ClusterUUID
		run_once()
	case 13:
		// Host SN
		data = *HostSN
	case 14:
		// Host MAC
		data = *HostMAC
		data = strings.ToLower(strings.ReplaceAll(data, "-", ":"))
	case 15:
		// Host model
		data = *HostModel
	case 16:
		// Update Dead line time, always 0x7fffffffffffffff
		data = "9223372036854775807"
	case 17:
		// TimeStamp
	default:
		log.Printf("No handler for command: %d\n", req.CommandID)
		return
	}

	// if it's a req and need a response
	if req.IsReq == 1 && req.NeedResponse == 1 {
		buf = make([]byte, 0, 4096)
		writer := bytes.NewBuffer(buf)
		req.IsResp = 1
		req.IsReq = 0
		req.ReqLength = 0
		req.RespLength = int32(len([]byte(data)) + 1)
		fmt.Printf("Response data: %s\n", data)

		// write to buf
		binary.Write(writer, binary.LittleEndian, &req)
		writer.Write([]byte(data))
		res := writer.Bytes()
		// full fill 4096
		buf = make([]byte, 4096, 4096)
		copy(buf, res)

		_, err := conn.Write(buf)

		if err != nil {
			log.Println("Write error:", err.Error())
			return
		}
	}
}

func home(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte(`{"status": "error", "data": null, "message": "No command specified"}`))
}

func read(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	Writer.Lock()
	defer Writer.Unlock()

	if Connection == nil || Chan == nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status": "error", "data": null, "message": "No connection to guest"}`))
		return
	}

	var err error
	var commandID int

	query := r.URL.Query()
	commandID, err = strconv.Atoi(query.Get("command"))

	if err != nil || commandID < 1 {
		log.Printf("Failed parsing command %s \n", query.Get("command"))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status": "error", "data": null, "message": "Invalid command ID"}`))
		return
	}

	fmt.Printf("Reading command: %d \n", commandID)
	atomic.StoreInt32(&WaitingFor, (int32)(commandID))

	if !send_command((int32)(commandID), 1, 1) {
		atomic.StoreInt32(&WaitingFor, 0)
		log.Printf("Failed reading command %d from guest \n", commandID)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status": "error", "data": null, "message": "Failed to read command"}`))
		return
	}

	var resp RESP

	select {
	case res := <-Chan:
		resp = res
	case <-time.After(15 * time.Second):
		atomic.StoreInt32(&WaitingFor, 0)
		log.Printf("Timeout while reading command %d from guest \n", commandID)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status": "error", "data": null, "message": "Received no response"}`))
		return
	}

	atomic.StoreInt32(&WaitingFor, 0)

	if resp.id != (int32)(commandID) {
		log.Printf("Received wrong response for command %d from guest: %d \n", commandID, resp.id)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status": "error", "data": null, "message": "Received wrong response"}`))
		return
	}

	if resp.id == 6 {
		resp.data = "null"
		Shutdown.Store(true)
	}

	if resp.data == "" {
		log.Printf("Received no data for command %d \n", commandID)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status": "error", "data": null, "message": "Received no data"}`))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "success", "data": ` + resp.data + `, "message": null}`))
	return
}

func write(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	Writer.Lock()
	defer Writer.Unlock()

	if Connection == nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status": "error", "data": null, "message": "No connection to guest"}`))
		return
	}

	var err error
	var commandID int

	query := r.URL.Query()
	commandID, err = strconv.Atoi(query.Get("command"))

	if err != nil || commandID < 1 {
		log.Printf("Failed parsing command %s \n", query.Get("command"))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status": "error", "data": null, "message": "Invalid command ID"}`))
		return
	}

	fmt.Printf("Sending command: %d \n", commandID)

	if !send_command((int32)(commandID), 1, 0) {
		log.Printf("Failed sending command %d to guest \n", commandID)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status": "error", "data": null, "message": "Failed to send command"}`))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "success", "data": null, "message": null}`))
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
	req.RandID = mrand.Int63()
	req.NeedResponse = needsResp

	var buf = make([]byte, 0, 4096)
	var writer = bytes.NewBuffer(buf)

	// write to buf
	binary.Write(writer, binary.LittleEndian, &req)
	res := writer.Bytes()

	// full fill 4096
	buf = make([]byte, 4096, 4096)
	copy(buf, res)

	//fmt.Printf("Writing command %d\n", CommandID)

	if Connection == nil { return false }

	_, err := Connection.Write(buf)

	if err != nil {
		log.Println("Write error:", err.Error())
		return false
	}

	return true
}

func uuid() string {

	b := make([]byte, 16)
	_, err := crand.Read(b)

	if err != nil {
		log.Println("Error on uuid:", err.Error())
		return "aa00bc73-4772-4fda-b134-c737485ff084"
	}

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%12x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func run_once() {

	if !Executed.Load() {
		Executed.Store(true)
		var file string
		file = path() + "/print.sh"
		if exists(file) { execute(file, nil) }
	}

}

func path() string {

	exePath, err := os.Executable() // Get the executable file's path

	if err != nil {
		log.Println("Path error:", err)
		return ""
	}

	dirPath := filepath.Dir(exePath) // Get the directory of the executable file

	return dirPath
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

	if err != nil {
		log.Println("Cannot run:", err.Error())
		return false
	}

	return true
}
