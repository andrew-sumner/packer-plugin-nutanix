package nutanix

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
)

// shutdownDriver fails GetVM with the context's error, as the v4 client does
// once the build context is cancelled. The embedded Driver is nil: any other
// method called by the test would panic.
type shutdownDriver struct {
	Driver
	getVMCalls int
}

func (d *shutdownDriver) GetVM(ctx context.Context, vmUUID string) (*nutanixInstance, error) {
	d.getVMCalls++
	return nil, ctx.Err()
}

// TestStepShutdownCancelledWhileWaiting checks that a cancelled build stops
// waiting for shutdown promptly instead of dereferencing the nil VM GetVM
// returns with its error.
func TestStepShutdownCancelledWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := &shutdownDriver{}
	state := new(multistep.BasicStateBag)
	state.Put("ui", &packer.BasicUi{Reader: new(bytes.Buffer), Writer: io.Discard, ErrorWriter: io.Discard})
	state.Put("driver", d)
	state.Put("communicator", new(packer.MockCommunicator))
	state.Put("config", &Config{})
	state.Put("vm_uuid", "vm-1")

	step := &StepShutdown{Timeout: time.Minute, DisableStopInstance: true}

	done := make(chan multistep.StepAction, 1)
	go func() { done <- step.Run(ctx, state) }()

	select {
	case action := <-done:
		if action != multistep.ActionHalt {
			t.Errorf("expected ActionHalt, got %v", action)
		}
		if _, ok := state.GetOk("error"); !ok {
			t.Error("expected an error in state")
		}
		if d.getVMCalls != 1 {
			t.Errorf("expected 1 GetVM call, got %d", d.getVMCalls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StepShutdown did not return promptly after the build was cancelled")
	}
}
