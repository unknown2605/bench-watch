package collect

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDefaultNameGatePassesRealNames(t *testing.T) {
	for _, s := range []string{
		"Google Chrome Helper (GPU)", "Code Helper (Renderer)", "com.docker.sailor", "com.apple.WebKit.WebContent",
		"AMPDeviceDiscoveryAgent", "XPCServiceHelper", "node", "2.1.34",
		"php84_service_1", "loading_balancer_service", "redis_slave_service_1", "web-services", "infrastructure-services",
		"en0", "utun3", "bridge100", "llw0", "awdl0", "disk0", "root", "data", "Macintosh HD", "Untitled 1", "x86_64",
		"iTerm2", "zoom.us", "deadbeef", "mariadb_service", "postgres_service", "x86_64",
		"ShipIt", "XProtectBridgeService", "appleh13camerad",
		"wifip2pd", "com.apple.DriverKit-AppleBCMWLAN", "AirPlayXPCHelper", "1Password",
		"python3.13", "macOS", "k8s", "s3bucket", "ubuntu2204", "PowerPointWidget_mac",
		"Google Chrome Helper (Renderer)", "Adobe Creative Cloud Experience", "Safari Web Content (Cached)",
		"launchd", "com.apple.xpc", "crashpad_handler", "gitstatusd-darwin-arm64", "msedgewebview2_crashpad_handler",
	} {
		if !DefaultNameGate(s) {
			t.Errorf("refused real name %q", s)
		}
	}
}

// Real names from this Mac that labels.LooksSecret alone misreads (CM-00009 QA-M1); macOS truncates them to 32 bytes.
func TestTheGatePassesRealCamelCaseAndServiceNames(t *testing.T) {
	for _, s := range []string{"IntelligencePlatformComputeServi", "mysqldb8_service", "infrastructure-services_mysqldb8",
		"WiFiAgent", "TGOnDeviceInferenceProviderServi", "VTDecoderXPCService", "mDNSResponder", "mDNSResponderHelper",
		"FindMyWidgetItems", "WallpaperMacintoshExtension", "BackgroundTaskManagementAgent",
		"AccessibilitySettingsSearchExten", "SiriSuggestionsBookkeepingServic", "php-fpm", "xpc-cmd",
		"web-services_php84_1_fpm", "WiFiMacSettingsIntents"} {
		if !DefaultNameGate(s) {
			t.Errorf("refused real name %q", s)
		}
	}
}

// fake assembles token-shaped test values at run time, so no token literal sits in the source.
func fake(parts ...string) string { return strings.Join(parts, "") }

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomFrom(r *rand.Rand, chars string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[r.IntN(len(chars))]
	}
	return string(b)
}

func TestDefaultNameGateRefusesSecretShapes(t *testing.T) {
	r := rand.New(rand.NewPCG(4, 2))
	hex64 := randomFrom(r, "0123456789abcdef", 64)
	values := []string{
		"", "/Users/someone/bin/tool", "name=value", "a\x00b", "Ana's disk", strings.Repeat("x", 65),
		"curl -H Authorization", "my secret password is hunter2", "Please Send The Token Now", "app ", "a  b",
		"-rf", "+cmd", "=SUM(A1)", "@user",
		fake("gh", "p_", randomFrom(r, alnum, 36)), fake("s", "k-", "proj-", randomFrom(r, alnum, 24)),
		fake("xo", "xb-", "1234567890-abcdef"), fake("AK", "IA", "IOSFODNN7EXAMPLE"),
		fake("AI", "za", randomFrom(r, alnum, 30)), fake("ey", "J", "hbGciOiJIUzI1NiJ9"), fake("sk", "_live_", "51Habc"),
		fake("github", "_pat_", "11ABCDEF"), fake("gl", "pat-", "abcdefghij"), fake("np", "m_", randomFrom(r, alnum, 20)),
		"123e4567-e89b-12d3-a456-426614174000", "123e4567e89b12d3a456426614174000", hex64, hex64[:12],
		"Zq8xT2mLp4Kv9RsW", "q7w2e9r4t1y6", "kJhGfDsAqWeRtYuI", "Ab3d-9Kx2-Lm4q-Pz7w",
		"k3j9x-p0q2m-z8v1n-w4r7t", "a8Kd9fQzLw", "token.ABCdef12", "12345678901",
		fake("QmnDHXe-", "JnY45OElPOMM8u"), fake("MYdLYFivy_", "TpY91H0Apz-P"), fake("GOJVFHQjXZ_", "3JedH-oxzgQ"),
		fake("py", "pi-", "AgEIcHlwaS5vcmc"), fake("ya", "29.", "a0AfH6SMBx"),
		strings.Join([]string{hex64[:11], hex64[11:22], hex64[22:33], hex64[33:44], hex64[44:55]}, "-"),
		strings.Join([]string{hex64[:11], hex64[11:22], hex64[22:33], hex64[33:44], hex64[44:55]}, "."),
		fake("sk", "-ant-", "api03-abc"), fake("np", "m_", "Abcdefghijklm"), fake("hf", "_", "Abcdefgh12"),
		"k3j9x-p0q2m-z8v1n-w4r7t", "xkcd-qrst-mnbv-a1b2", "a7f3-zxqw", "plmk.vbnc.2025q",
		fake("Summer", "2024"), fake("Wel", "come1"), fake("hun", "ter2"), fake("Pass", "word123"), fake("pass", "w0rd"),
		fake("trust", "no1"), fake("let", "mein"), fake("summer", "-2024"), fake("s", "k-ant"), fake("s", "k-proj"),
		"192.168.1.10", "10.0.0.1", "1234567", "12345", "ab-cd-ef-gh-ij",
	}
	for _, s := range values {
		if DefaultNameGate(s) {
			t.Errorf("passed %q", s)
		}
	}
	passed := 0
	const draws = 2000
	for range draws {
		if DefaultNameGate(randomFrom(r, alnum, 20+r.IntN(30))) {
			passed++
		}
	}
	if passed > draws/100 {
		t.Errorf("%d of %d random tokens of 20-49 characters passed", passed, draws)
	}
}

func TestNameGateRefusesAddressesBesideADot(t *testing.T) {
	for _, s := range []string{"host.192.168.1.10", "192.168.1.10.local", "db.10.0.0.5.svc", "node-10.0.0.5"} {
		if DefaultNameGate(s) {
			t.Errorf("passed %q", s)
		}
	}
	for _, s := range []string{"2.1.34", "v1.2.3", "python3.13", "node-24.22.2", "1.2.3.4.5", "app.2.1.34"} {
		if !DefaultNameGate(s) {
			t.Errorf("refused version %q", s)
		}
	}
}

// Card, SSN and phone shapes split by separators; seeded digits, so no real number sits in the source.
func TestNameGateRefusesDigitGroups(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 9))
	digits := func(n int) string { return randomFrom(r, "0123456789", n) }
	for range 2000 {
		for _, s := range []string{
			digits(4) + "-" + digits(4) + "-" + digits(4) + "-" + digits(4), digits(4) + " " + digits(4) + " " + digits(4),
			digits(3) + "-" + digits(2) + "-" + digits(4), digits(3) + "-" + digits(3) + "-" + digits(4),
			"(" + digits(3) + ") " + digits(3) + "-" + digits(4), digits(3) + "." + digits(3) + "." + digits(4),
			"call_" + digits(3) + "_" + digits(3) + "_" + digits(4),
		} {
			if DefaultNameGate(s) {
				t.Fatalf("passed %q", s)
			}
		}
	}
	for _, s := range []string{"2024-01-15", "x86_64", "web-services-php84_service_1-1", "ubuntu2204"} {
		if !DefaultNameGate(s) {
			t.Errorf("refused real name %q", s)
		}
	}
}

// Real daemon names near the letter-pair floor; the floor was set so they pass (CM-00009 R2-1).
func TestTheGatePassesOneCaseDaemonNames(t *testing.T) {
	for _, s := range []string{"fseventsd", "mdworker", "launchservicesd", "kworker", "mdbulkimport", "sandboxd",
		"phpmyadmin", "feedbackd", "clocksyncd", "watchdogd", "vpnagentd", "followupd", "symptomsd", "distnoted",
		"nsurlsessiond", "usbmuxd", "apfsd", "autofsd", "backupd", "keybagd", "mlhostd", "ctkahp", "fskitd",
		"com.apple.fskit.msdos", "backgroundtaskmanagementd", "MACINTOSH HD", "CPU", "NVME"} {
		if !DefaultNameGate(s) {
			t.Errorf("refused real name %q", s)
		}
	}
}

func TestNameGateStages(t *testing.T) {
	refuseAll := NewNameGate(func(string) bool { return true })
	passAll := NewNameGate(func(string) bool { return false })
	if refuseAll("node") || NewNameGate(nil)("node") || !passAll("q7w2e9r4t1y6") {
		t.Fatal("the second stage is not the one given")
	}
	for _, s := range []string{"/Users/x", "123e4567-e89b-12d3-a456-426614174000", fake("gh", "p_", "abc"), "a b"} {
		if passAll(s) {
			t.Errorf("the shape checks must refuse %q whatever the second stage says", s)
		}
	}
	saved := secondStage
	defer func() { secondStage = saved }()
	secondStage = func(s string) bool { return s == "node" }
	if DefaultNameGate("node") || !DefaultNameGate("q7w2e9r4t1y6") {
		t.Fatal("DefaultNameGate does not run secondStage")
	}
}

func TestNameGateName(t *testing.T) {
	strict := NameGate(func(s string) bool { return s == "ok" })
	if strict.Name("ok") != "ok" || strict.Name("nope") != Other {
		t.Fatal("custom gate not applied")
	}
	var unset NameGate
	if unset.Name("123e4567-e89b-12d3-a456-426614174000") != Other || unset.Name("en0") != "en0" {
		t.Fatal("a nil gate must be the default")
	}
	if unset.Name(Other) != OtherReal || DefaultNameGate(OtherReal) || strict.Name(Other) != Other {
		t.Fatal("a real name spelled like Other must keep its own row")
	}
}

type coded string

func (c coded) Error() string { return "/secret/path refused" }
func (c coded) Code() string  { return string(c) }

type permanent struct{}

func (permanent) Error() string   { return "check constraint on /Users/x" }
func (permanent) Permanent() bool { return true }

func TestErrorCodeNeverCarriesText(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("dial unix /Users/x/.docker/run/docker.sock: %w", syscall.ENOENT), "ENOENT"},
		{fmt.Errorf("connect: %w", syscall.ECONNREFUSED), "ECONNREFUSED"},
		{context.DeadlineExceeded, "ETIMEDOUT"},
		{fmt.Errorf("wrapped: %w", coded("EGUARD")), "EGUARD"},
		{coded("/Users/x/secret"), "EOTHER"},
		{coded("password=hunter2"), "EOTHER"},
		{fmt.Errorf("x: %w", ErrPermanent), "EPERMANENT"},
		{permanent{}, "EPERMANENT"},
		{errors.New("password=hunter2"), "EOTHER"},
		{nil, ""},
	}
	for _, c := range cases {
		got := ErrorCode(c.err)
		if got != c.want {
			t.Errorf("ErrorCode(%v) = %q, want %q", c.err, got, c.want)
		}
		if got != "" && !slices.Contains(Codes, got) {
			t.Errorf("code %q outside the fixed set", got)
		}
	}
}

func TestIsPermanent(t *testing.T) {
	if !IsPermanent(fmt.Errorf("a: %w", ErrPermanent)) || !IsPermanent(fmt.Errorf("b: %w", permanent{})) {
		t.Fatal("permanent errors not recognised")
	}
	if IsPermanent(syscall.ECONNREFUSED) || IsPermanent(nil) {
		t.Fatal("a transient error read as permanent")
	}
}

func TestStatusTransitions(t *testing.T) {
	now := time.Unix(1000, 0)
	s := NewStatus(func() time.Time { return now })
	s.Fail(SourceDocker, StateUnavailable, syscall.ENOENT)
	if g := s.Get(SourceDocker); g.State != StateUnavailable || g.LastError != "ENOENT" || !g.Since.Equal(now) {
		t.Fatalf("after fail: %+v", g)
	}
	now = now.Add(time.Minute)
	s.OK(SourceDocker)
	g := s.Get(SourceDocker)
	if g.State != StateOK || !g.Since.Equal(now) || !g.LastOK.Equal(now) || g.LastError != "ENOENT" {
		t.Fatalf("after recovery: %+v", g)
	}
	s.Report(SourceMachine, []PartError{NewPartError(PartMemory, syscall.EPERM), NewPartError(PartNet, syscall.EIO),
		NewPartError(PartMemory, syscall.EPERM)})
	g = s.Get(SourceMachine)
	if g.State != StatePartial || g.LastError != "EPERM" || !slices.Equal(g.Missing, []string{PartMemory, PartNet}) {
		t.Fatalf("partial: %+v", g)
	}
	s.Report(SourceMachine, nil)
	if g = s.Get(SourceMachine); g.State != StateOK || g.Missing != nil {
		t.Fatalf("after partial recovery: %+v", g)
	}
	s.AddDrops(SourceHistory, Drops{Rejected: 2, Expired: 3})
	s.AddDrops(SourceHistory, Drops{Overflow: 1})
	if d := s.Get(SourceHistory).Drops; d != (Drops{Rejected: 2, Overflow: 1, Expired: 3}) {
		t.Fatalf("drops %+v", d)
	}
}

func TestMemSinkKeepsEachRowOnce(t *testing.T) {
	var m MemSink
	t0 := time.Unix(100, 0)
	b := Batch{Key: Key{SourceMachine, t0}, Machine: []MachineSample{{At: t0}, {At: t0.Add(time.Second)}}}
	m.FailNext(1, syscall.ECONNREFUSED)
	if err := m.Write(context.Background(), b); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("want refusal, got %v", err)
	}
	for range 2 {
		if err := m.Write(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	if mc, _, _ := m.Totals(); mc != 2 || m.Duplicates() != 2 || m.Writes() != 3 {
		t.Fatalf("rows %d duplicates %d writes %d", mc, m.Duplicates(), m.Writes())
	}
}

func TestRealClockIsWallTimeInUTC(t *testing.T) {
	now := RealClock().Now()
	if now.Location() != time.UTC || strings.Contains(now.String(), "m=") {
		t.Fatalf("clock reads %s", now)
	}
}
