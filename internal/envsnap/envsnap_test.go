package envsnap

import (
	"reflect"
	"testing"
)

func TestCaptureKeepsOnlyAllowListedVariables(t *testing.T) {
	got := Capture([]string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/dev",
		"LC_ALL=en_US.UTF-8",
		"HTTPS_PROXY=http://proxy:3128",
		"GH_TOKEN=ghp_x",
		"SECRET_DB_PASSWORD=hunter2",
		"AWS_SECRET_ACCESS_KEY=abc",
		"ARCHWRIGHT_RUN=run-1",    // run identity is the adapter's, never inherited
		"ARCHWRIGHT_RUN_TOKEN=tk", //
		"TMUX=/tmp/tmux-1/default,1,0",
		"TMUX_PANE=%3",
		"GIT_DIR=/elsewhere/.git", // would redirect every git command in the Session
		"malformed",
		"=novalue",
	})
	want := map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": "/home/dev", "LC_ALL": "en_US.UTF-8",
		"HTTPS_PROXY": "http://proxy:3128", "GH_TOKEN": "ghp_x",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %v\nwant %v", got, want)
	}
}

func TestCaptureKeepsAnEmptyValueAndEqualsInsideAValue(t *testing.T) {
	got := Capture([]string{"TERM=", "PATH=/a:/b", "LANG=a=b"})
	if v, ok := got["TERM"]; !ok || v != "" {
		t.Errorf("TERM = %q, %v", v, ok)
	}
	if got["LANG"] != "a=b" {
		t.Errorf("LANG = %q", got["LANG"])
	}
}

func TestHolderServesTheLatestSnapshotAndACopy(t *testing.T) {
	h := NewHolder([]string{"PATH=/first", "SECRET=x"})
	if got := h.Get(); got["PATH"] != "/first" || len(got) != 1 {
		t.Fatalf("initial = %v", got)
	}
	got := h.Get()
	got["PATH"] = "tampered"
	if h.Get()["PATH"] != "/first" {
		t.Fatal("a caller changed the held snapshot")
	}
	h.Set(map[string]string{"PATH": "/second", "SECRET": "y", "ARCHWRIGHT_RUN": "r"})
	if got := h.Get(); got["PATH"] != "/second" || len(got) != 1 {
		t.Fatalf("after Set = %v (Set must filter too: a client's word is not trusted)", got)
	}
}

func TestEncodeAndDecodeRoundTripForTheWire(t *testing.T) {
	in := Capture([]string{"PATH=/x", "HOME=/h", "NOPE=1"})
	hdr := Encode(in)
	out, err := Decode(hdr)
	if err != nil || !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %v, %v", out, err)
	}
	if _, err := Decode("not base64 json!"); err == nil {
		t.Error("garbage decoded")
	}
}
