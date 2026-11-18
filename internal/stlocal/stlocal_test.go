package stlocal

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(TokenHeader) != "good" || r.URL.Path != "/api/version" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	dir := t.TempDir()
	t.Setenv("STUI_ST_HOME", dir)
	write := func(pid int, token string) {
		os.WriteFile(filepath.Join(dir, "agent.json"), []byte(fmt.Sprintf(`{"pid":%d,"port":%d,"token":%q}`, pid, port, token)), 0o600)
	}

	if Live() {
		t.Fatal("no record: live")
	}
	write(0, "good")
	if Live() {
		t.Fatal("dead pid: live")
	}
	write(os.Getpid(), "bad")
	if Find() == nil || Live() {
		t.Fatal("bad token: want a record that does not answer")
	}
	write(os.Getpid(), "good")
	if !Live() {
		t.Fatal("good record: not live")
	}
}
