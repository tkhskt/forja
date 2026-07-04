package adb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeExecutor records calls and returns canned responses keyed by a substring
// of the joined args. It panics if the requested args don't match any rule —
// that flushes out unexpected adb calls early.
type fakeExecutor struct {
	t      *testing.T
	calls  []fakeCall
	canned []cannedResponse
}

type fakeCall struct {
	name  string
	args  []string
	stdin []byte
}

type cannedResponse struct {
	matchSubstr string // matches if the joined args contain this substring
	stdout      string
	stderr      string
	err         error
}

func (f *fakeExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	return f.dispatch(name, args, nil)
}

func (f *fakeExecutor) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	return f.dispatch(name, args, stdin)
}

func (f *fakeExecutor) dispatch(name string, args []string, stdin []byte) ([]byte, []byte, error) {
	joined := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, fakeCall{name: name, args: args, stdin: stdin})
	for _, c := range f.canned {
		if strings.Contains(joined, c.matchSubstr) {
			return []byte(c.stdout), []byte(c.stderr), c.err
		}
	}
	f.t.Fatalf("unexpected adb call: %q", joined)
	return nil, nil, errors.New("unreachable")
}

func TestValidateApp(t *testing.T) {
	good := []string{"com.example", "com.foo.bar", "a.b", "x123.y_z.q"}
	bad := []string{"", "noDot", ".com.foo", "com.", "com..foo", "1com.foo", "com.foo/bar"}
	for _, g := range good {
		if err := ValidateApp(g); err != nil {
			t.Errorf("ValidateApp(%q) unexpectedly failed: %v", g, err)
		}
	}
	for _, b := range bad {
		if err := ValidateApp(b); err == nil {
			t.Errorf("ValidateApp(%q) should have failed", b)
		}
	}
}

func TestRunAsWriteRejectsBadApp(t *testing.T) {
	fx := &fakeExecutor{t: t}
	a := NewWithExecutor(fx)
	err := a.RunAsWrite(context.Background(), "not_an_app", "files/x", []byte("x"))
	if err == nil {
		t.Fatal("expected error for invalid applicationId")
	}
	if len(fx.calls) != 0 {
		t.Errorf("should not have invoked adb for invalid app")
	}
}

func TestRunAsWriteSendsExpectedShellLine(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "run-as com.example", stdout: "", stderr: ""},
		},
	}
	a := NewWithExecutor(fx)
	if err := a.RunAsWrite(context.Background(), "com.example", "files/rules.json", []byte("[]")); err != nil {
		t.Fatalf("RunAsWrite: %v", err)
	}
	if len(fx.calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(fx.calls))
	}
	c := fx.calls[0]
	if c.name != "adb" || c.args[0] != "shell" {
		t.Errorf("wrong command: %v %v", c.name, c.args)
	}
	if string(c.stdin) != "[]" {
		t.Errorf("stdin not forwarded: %q", c.stdin)
	}
	shellLine := c.args[1]
	for _, want := range []string{
		"run-as com.example",
		"sh -c",
		"mkdir -p $(dirname files/rules.json)",
		"rm -f files/rules.json",
		"cat > files/rules.json",
		"chmod 400 files/rules.json",
	} {
		if !strings.Contains(shellLine, want) {
			t.Errorf("shell line missing %q\nfull: %s", want, shellLine)
		}
	}
}

func TestRunAsReadMissingReturnsNilNil(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "run-as com.example cat", stdout: "", stderr: ""},
		},
	}
	a := NewWithExecutor(fx)
	out, err := a.RunAsRead(context.Background(), "com.example", "files/missing")
	if err != nil {
		t.Fatalf("RunAsRead: %v", err)
	}
	if out != nil {
		t.Errorf("want nil out, got %q", out)
	}
}

func TestRunAsReadReturnsContent(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "run-as com.example cat", stdout: `[{"name":"x"}]`, stderr: ""},
		},
	}
	a := NewWithExecutor(fx)
	out, err := a.RunAsRead(context.Background(), "com.example", "files/rules.json")
	if err != nil {
		t.Fatalf("RunAsRead: %v", err)
	}
	if string(out) != `[{"name":"x"}]` {
		t.Errorf("want content preserved, got %q", out)
	}
}

func TestPidofReturnsZeroWhenNotRunning(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			// pidof exits 1 when process is not running
			{matchSubstr: "pidof com.example", stdout: "", stderr: "", err: errors.New("exit 1")},
		},
	}
	a := NewWithExecutor(fx)
	pid, err := a.Pidof(context.Background(), "com.example")
	if err != nil {
		t.Fatalf("Pidof: %v", err)
	}
	if pid != 0 {
		t.Errorf("want pid=0 for not running, got %d", pid)
	}
}

func TestPidofParsesFirstFieldOnly(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "pidof", stdout: "12345 67890\n"},
		},
	}
	a := NewWithExecutor(fx)
	pid, err := a.Pidof(context.Background(), "com.example")
	if err != nil {
		t.Fatalf("Pidof: %v", err)
	}
	if pid != 12345 {
		t.Errorf("want pid=12345, got %d", pid)
	}
}

func TestPrimaryABI(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "getprop ro.product.cpu.abi", stdout: "arm64-v8a\n"},
		},
	}
	a := NewWithExecutor(fx)
	abi, err := a.PrimaryABI(context.Background())
	if err != nil {
		t.Fatalf("PrimaryABI: %v", err)
	}
	if abi != "arm64-v8a" {
		t.Errorf("want arm64-v8a, got %q", abi)
	}
}

func TestListDebuggableAppsParsesLines(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "/proc/[0-9]", stdout: "com.tkhskt.sample_app\ncom.example.other\n"},
		},
	}
	a := NewWithExecutor(fx)
	apps, err := a.ListDebuggableApps(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 2 || apps[0] != "com.tkhskt.sample_app" || apps[1] != "com.example.other" {
		t.Errorf("unexpected: %v", apps)
	}
}

func TestListDebuggableAppsFiltersGarbage(t *testing.T) {
	// Defensive: even if device echoes something the grep should have filtered,
	// ValidateApp drops it before bubbling up to the caller.
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "/proc/[0-9]", stdout: "com.ok\n!!!garbage!!!\nnodots\n"},
		},
	}
	a := NewWithExecutor(fx)
	apps, _ := a.ListDebuggableApps(context.Background())
	if len(apps) != 1 || apps[0] != "com.ok" {
		t.Errorf("expected single valid applicationId, got %v", apps)
	}
}

func TestForegroundAppExtractsFromDumpsys(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "dumpsys activity",
				stdout: "  topResumedActivity = ActivityRecord{abc u0 com.tkhskt.sample_app/.MainActivity t123}\n"},
		},
	}
	a := NewWithExecutor(fx)
	app, err := a.ForegroundApp(context.Background())
	if err != nil {
		t.Fatalf("Foreground: %v", err)
	}
	if app != "com.tkhskt.sample_app" {
		t.Errorf("want com.tkhskt.sample_app, got %q", app)
	}
}

func TestForegroundAppEmptyWhenNothingMatches(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "dumpsys activity", stdout: "nothing useful here\n"},
		},
	}
	a := NewWithExecutor(fx)
	app, err := a.ForegroundApp(context.Background())
	if err != nil {
		t.Fatalf("Foreground: %v", err)
	}
	if app != "" {
		t.Errorf("want empty, got %q", app)
	}
}

func TestAttachAgentSendsExpectedArg(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "attach-agent com.example", stdout: ""},
		},
	}
	a := NewWithExecutor(fx)
	err := a.AttachAgent(context.Background(), "com.example",
		"/data/data/com.example/files/agent.so",
		"/data/data/com.example/files/agent.dex")
	if err != nil {
		t.Fatalf("AttachAgent: %v", err)
	}
	if len(fx.calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(fx.calls))
	}
	got := strings.Join(fx.calls[0].args, " ")
	for _, want := range []string{
		"cmd activity attach-agent com.example",
		"/data/data/com.example/files/agent.so=/data/data/com.example/files/agent.dex",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in call: %s", want, got)
		}
	}
}

func TestAttachAgentDetectsExceptionInStdout(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "attach-agent", stdout: "java.lang.RuntimeException: agent dlopen failed"},
		},
	}
	a := NewWithExecutor(fx)
	err := a.AttachAgent(context.Background(), "com.example", "/so", "/dex")
	if err == nil {
		t.Error("expected error when stdout contains Exception")
	}
}

func TestParseDevices(t *testing.T) {
	out := `List of devices attached
emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 transport_id:1
RZ8N70ABCDE            device usb:1-1 product:panther model:Pixel_7 transport_id:2
00fabc                 unauthorized
192.168.1.5:5555       offline

`
	got := parseDevices(out)
	if len(got) != 4 {
		t.Fatalf("want 4 devices, got %d: %+v", len(got), got)
	}
	if got[0].Serial != "emulator-5554" || got[0].State != "device" || got[0].Model != "sdk_gphone64_arm64" {
		t.Errorf("device[0] parsed wrong: %+v", got[0])
	}
	if got[1].Serial != "RZ8N70ABCDE" || got[1].Model != "Pixel_7" {
		t.Errorf("device[1] model not lifted: %+v", got[1])
	}
	if got[2].State != "unauthorized" || got[2].Model != "" {
		t.Errorf("device[2] should be unauthorized with no model: %+v", got[2])
	}
	if got[3].State != "offline" {
		t.Errorf("device[3] should be offline: %+v", got[3])
	}
}

func TestParseDevicesEmpty(t *testing.T) {
	if d := parseDevices("List of devices attached\n\n"); len(d) != 0 {
		t.Errorf("no devices should parse to empty, got %+v", d)
	}
}

func TestDevicesQueriesGlobalTable(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "devices -l", stdout: "List of devices attached\nemulator-5554\tdevice\n"},
		},
	}
	// Even with a serial target set, Devices() lists the global table and must
	// NOT pass -s (it's not a device-scoped command).
	a := NewWithExecutorSerial(fx, "emulator-5554")
	devs, err := a.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devs) != 1 || devs[0].Serial != "emulator-5554" {
		t.Fatalf("unexpected devices: %+v", devs)
	}
	joined := strings.Join(fx.calls[0].args, " ")
	if strings.Contains(joined, "-s") {
		t.Errorf("Devices() must not pass -s; got args %v", fx.calls[0].args)
	}
}

func TestSerialPrependedToDeviceScopedCalls(t *testing.T) {
	fx := &fakeExecutor{
		t: t,
		canned: []cannedResponse{
			{matchSubstr: "pidof", stdout: "1234\n"},
		},
	}
	a := NewWithExecutorSerial(fx, "emulator-5554")
	if _, err := a.Pidof(context.Background(), "com.example.app"); err != nil {
		t.Fatalf("Pidof: %v", err)
	}
	args := fx.calls[0].args
	if len(args) < 2 || args[0] != "-s" || args[1] != "emulator-5554" {
		t.Fatalf("expected -s emulator-5554 prefix, got %v", args)
	}
	if args[2] != "shell" {
		t.Errorf("shell should follow the -s target, got %v", args)
	}
}

func TestNoSerialNoDashS(t *testing.T) {
	fx := &fakeExecutor{
		t:      t,
		canned: []cannedResponse{{matchSubstr: "pidof", stdout: "1\n"}},
	}
	a := NewWithExecutor(fx) // no serial
	if _, err := a.Pidof(context.Background(), "com.example.app"); err != nil {
		t.Fatalf("Pidof: %v", err)
	}
	if fx.calls[0].args[0] != "shell" {
		t.Errorf("with no serial, args should start at shell, got %v", fx.calls[0].args)
	}
}
