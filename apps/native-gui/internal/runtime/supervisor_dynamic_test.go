package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedChildDynamicAddress(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WT_NATIVE_GUI_CONFIG_DIR", dir)
	api, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	socks, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socks.Close()
	original := fmt.Sprintf(`{"listen_api":%q,"socks_listen":%q,"client_id":"preserved"}`, api.Addr().String(), socks.Addr().String())
	config := filepath.Join(dir, "original.json")
	if err = os.WriteFile(config, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "daemon.sh")
	executable, _ := os.Executable()
	if err = os.WriteFile(binary, []byte(fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestDynamicDaemonHelper -- \"$@\"\n", executable)), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_DYNAMIC_TEST_CHILD", "1")
	supervisor, err := NewDaemonSupervisor(RuntimeResourceSummary{RuntimeAPIURL: "http://" + api.Addr().String(), Candidates: []RuntimeResourceCandidate{
		{Kind: ResourceDaemonBinary, Target: binary, Status: "found", Exists: true},
		{Kind: ResourceDaemonConfig, Target: config, Status: "found", Exists: true},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := supervisor.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	if status.State != "running" || status.APIURL == "http://"+api.Addr().String() || announcedAPI("whitetransportd api listening on "+status.APIURL) == "" {
		t.Fatalf("wrong address: %+v", status)
	}
	oldPID := status.PID
	status, err = supervisor.Restart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.PID == oldPID {
		t.Fatal("restart did not replace the owned process")
	}
	raw, _ := os.ReadFile(config)
	if string(raw) != original {
		t.Fatal("original config changed")
	}
	response, err := http.Get(status.APIURL + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
}

func TestDynamicDaemonHelper(t *testing.T) {
	if os.Getenv("WT_DYNAMIC_TEST_CHILD") != "1" {
		return
	}
	var path string
	for i, arg := range os.Args {
		if arg == "--config" {
			path = os.Args[i+1]
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var config map[string]string
	if err = json.Unmarshal(raw, &config); err != nil {
		panic(err)
	}
	if config["listen_api"] != "127.0.0.1:0" || config["socks_listen"] != "127.0.0.1:0" || config["client_id"] != "preserved" {
		panic("launch config lost settings")
	}
	socks, err := net.Listen("tcp", config["socks_listen"])
	if err != nil {
		panic(err)
	}
	defer socks.Close()
	api, err := net.Listen("tcp", config["listen_api"])
	if err != nil {
		panic(err)
	}
	fmt.Printf("whitetransportd api listening on http://%s\n", api.Addr())
	_ = http.Serve(api, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"state":"off"}`) }))
	os.Exit(0)
}

func TestAnnouncedAPIRejectsForeignAndInvalidAddresses(t *testing.T) {
	for _, address := range []string{"http://192.0.2.1:80", "http://localhost:12", "http://127.0.0.1:0", "http://127.0.0.1:99999", "https://127.0.0.1:12", "http://user@127.0.0.1:12", "http://127.0.0.1:12/path"} {
		if got := announcedAPI("whitetransportd api listening on " + address); got != "" {
			t.Errorf("accepted %s", address)
		}
	}
}
