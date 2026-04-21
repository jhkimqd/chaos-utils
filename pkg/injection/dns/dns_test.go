package dns

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeSidecar struct {
	existingSidecar   bool
	qdiscStates       []string // return values for successive "tc qdisc show" calls
	qdiscIdx          int
	captured          [][]string
	execErr           map[string]error // cmd keyword -> err to return
	createSidecarErr  error
	qdiscShowCalled   int
}

func (f *fakeSidecar) CreateSidecar(ctx context.Context, cid string) (string, error) {
	f.existingSidecar = true
	return "sidecar-" + cid, f.createSidecarErr
}

func (f *fakeSidecar) ExecInSidecar(ctx context.Context, cid string, cmd []string) (string, error) {
	f.captured = append(f.captured, append([]string(nil), cmd...))
	joined := strings.Join(cmd, " ")
	for key, err := range f.execErr {
		if strings.Contains(joined, key) {
			return "", err
		}
	}
	// tc qdisc show — return the next programmed state
	if len(cmd) >= 3 && cmd[0] == "tc" && cmd[1] == "qdisc" && cmd[2] == "show" {
		f.qdiscShowCalled++
		if f.qdiscIdx < len(f.qdiscStates) {
			out := f.qdiscStates[f.qdiscIdx]
			f.qdiscIdx++
			return out, nil
		}
		return "qdisc noqueue 0: root refcnt 2\n", nil
	}
	return "", nil
}

func (f *fakeSidecar) GetSidecarID(cid string) (string, bool) {
	if f.existingSidecar {
		return "sidecar-" + cid, true
	}
	return "", false
}

func TestInjectDNSDelay_InstallsPrioWhenAbsent(t *testing.T) {
	sc := &fakeSidecar{
		existingSidecar: true,
		qdiscStates:     []string{"qdisc noqueue 0: root refcnt 2\n"},
	}
	dw := New(sc)
	if err := dw.InjectDNSDelay(context.Background(), "abcdef1234567890", DNSParams{DelayMs: 100}); err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	var sawPrioAdd bool
	for _, c := range sc.captured {
		if strings.Contains(strings.Join(c, " "), "qdisc add dev eth0 root handle 1: prio") {
			sawPrioAdd = true
		}
	}
	if !sawPrioAdd {
		t.Error("expected the injector to install a prio root when none exists")
	}
	// Track that we installed it (exported via Remove behavior below)
	dw.mu.Lock()
	if !dw.installedPrio["abcdef1234567890"] {
		t.Error("installedPrio[target1] should be true after we installed it")
	}
	dw.mu.Unlock()
}

func TestInjectDNSDelay_ReusesExistingPrio(t *testing.T) {
	sc := &fakeSidecar{
		existingSidecar: true,
		qdiscStates:     []string{"qdisc prio 1: root refcnt 13 bands 3\n"},
	}
	dw := New(sc)
	if err := dw.InjectDNSDelay(context.Background(), "abcdef1234567890", DNSParams{DelayMs: 100}); err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	for _, c := range sc.captured {
		if strings.Contains(strings.Join(c, " "), "qdisc add dev eth0 root handle 1: prio") {
			t.Errorf("should NOT reinstall prio root when one already exists, got cmd: %v", c)
		}
	}
	dw.mu.Lock()
	if dw.installedPrio["abcdef1234567890"] {
		t.Error("installedPrio[target1] should be false when we reused an existing prio")
	}
	dw.mu.Unlock()
}

func TestInjectDNSDelay_RefusesNetemRoot(t *testing.T) {
	sc := &fakeSidecar{
		existingSidecar: true,
		qdiscStates:     []string{"qdisc netem 8002: root refcnt 13 delay 100ms\n"},
	}
	dw := New(sc)
	err := dw.InjectDNSDelay(context.Background(), "abcdef1234567890", DNSParams{DelayMs: 100})
	if err == nil {
		t.Fatal("expected error when a netem root is already installed")
	}
	if !strings.Contains(err.Error(), "netem qdisc is already installed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestInjectDNSDelay_FailsOnQdiscInspectError(t *testing.T) {
	// Previously the inspect error was swallowed (`_` assignment), causing
	// inject to proceed as if the netns was empty. Make sure that's fixed.
	sc := &fakeSidecar{
		existingSidecar: true,
		execErr:         map[string]error{"tc qdisc show": fmt.Errorf("sidecar gone")},
	}
	dw := New(sc)
	err := dw.InjectDNSDelay(context.Background(), "abcdef1234567890", DNSParams{DelayMs: 100})
	if err == nil {
		t.Fatal("expected error when tc qdisc show fails")
	}
	if !strings.Contains(err.Error(), "failed to inspect tc state") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRemoveFault_DeletesPrioWhenWeInstalledIt(t *testing.T) {
	sc := &fakeSidecar{
		existingSidecar: true,
		qdiscStates: []string{
			"qdisc noqueue 0: root refcnt 2\n",        // pre-inject inspect
			"qdisc noqueue 0: root refcnt 2\n",        // post-remove verify
		},
	}
	dw := New(sc)
	if err := dw.InjectDNSDelay(context.Background(), "abcdef1234567890", DNSParams{DelayMs: 100}); err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	sc.captured = nil // reset to examine cleanup only
	if err := dw.RemoveFault(context.Background(), "abcdef1234567890"); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	var sawRootDel bool
	for _, c := range sc.captured {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "tc qdisc del dev eth0 root") && !strings.Contains(joined, "parent") {
			sawRootDel = true
		}
	}
	if !sawRootDel {
		t.Error("RemoveFault should delete the root prio qdisc when InjectDNSDelay installed it")
	}
	dw.mu.Lock()
	if dw.installedPrio["abcdef1234567890"] {
		t.Error("installedPrio[target1] should be cleared after RemoveFault")
	}
	dw.mu.Unlock()
}

func TestRemoveFault_LeavesPrioWhenPreExisting(t *testing.T) {
	sc := &fakeSidecar{
		existingSidecar: true,
		qdiscStates: []string{
			"qdisc prio 1: root refcnt 13 bands 3\n", // pre-inject (reused)
			"qdisc prio 1: root refcnt 13 bands 3\n", // post-remove verify
		},
	}
	dw := New(sc)
	if err := dw.InjectDNSDelay(context.Background(), "abcdef1234567890", DNSParams{DelayMs: 100}); err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	sc.captured = nil
	if err := dw.RemoveFault(context.Background(), "abcdef1234567890"); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	for _, c := range sc.captured {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "tc qdisc del dev eth0 root") && !strings.Contains(joined, "parent") {
			t.Errorf("RemoveFault must NOT delete root prio when a peer fault installed it, got cmd: %v", c)
		}
	}
}

func TestRemoveFault_DetectsResidualHandle(t *testing.T) {
	// Simulate verify showing the DNS netem is still present after cleanup.
	sc := &fakeSidecar{
		existingSidecar: true,
		qdiscStates: []string{
			"qdisc noqueue 0: root refcnt 2\n",                          // pre-inject
			"qdisc prio 1: root refcnt 13 bands 3\nqdisc netem 30: parent 1:3 limit 1000 delay 100ms\n", // post-remove verify — handle 30: still there
		},
	}
	dw := New(sc)
	if err := dw.InjectDNSDelay(context.Background(), "abcdef1234567890", DNSParams{DelayMs: 100}); err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	err := dw.RemoveFault(context.Background(), "abcdef1234567890")
	if err == nil {
		t.Fatal("expected error when the DNS netem handle 30: is still present after cleanup")
	}
	if !strings.Contains(err.Error(), "handle 30:") {
		t.Errorf("unexpected error: %v", err)
	}
}
