package nutanix

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

// This step shuts down the machine. It first attempts to do so gracefully,
// but ultimately forcefully shuts it down if that fails.
//
// Uses:
//
//	communicator packersdk.Communicator
//	driver Driver
//	ui     packersdk.Ui
//	vmName string
//
// Produces:
//
//	<nothing>
type StepShutdown struct {
	Command             string
	Timeout             time.Duration
	DisableStopInstance bool

	// pollInterval is how often the VM's power state is checked; zero means
	// defaultShutdownPollInterval. Set by tests.
	pollInterval time.Duration
}

const defaultShutdownPollInterval = 15 * time.Second

func (s *StepShutdown) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	comm := state.Get("communicator").(packersdk.Communicator)
	driver := state.Get("driver").(Driver)
	ui := state.Get("ui").(packersdk.Ui)
	config := state.Get("config").(*Config)
	vmUUID := state.Get("vm_uuid").(string)

	// commandErr holds a shutdown_command error that may only mean the
	// command powered the VM off and dropped the connection (for example
	// sysprep /shutdown over WinRM). It is reported if the VM never stops.
	var commandErr error

	if !s.DisableStopInstance {

		if config.Comm.Type == "none" {
			ui.Say("No Communicator configured, halting the virtual machine...")
			if err := powerOffOrAlreadyOff(ctx, driver, vmUUID); err != nil {
				err := fmt.Errorf("error stopping VM: %s", err)
				state.Put("error", err)
				ui.Error(err.Error())
				return multistep.ActionHalt
			}

		} else if s.Command != "" {
			ui.Say("Gracefully halting virtual machine...")
			log.Printf("executing shutdown command: %s", s.Command)
			cmd := &packersdk.RemoteCmd{Command: s.Command}
			if err := cmd.RunWithUi(ctx, comm, ui); err != nil {
				if ctx.Err() != nil {
					err := fmt.Errorf("build cancelled while running shutdown command: %w", err)
					state.Put("error", err)
					ui.Error(err.Error())
					return multistep.ActionHalt
				}
				// WinRM runs the command synchronously, so a connection dropped
				// by the shutdown itself is reported as an error that cannot be
				// told apart from a command that never ran: wait for the VM to
				// stop, and report this error if it does not. Other
				// communicators (SSH) report a dropped connection as an exit
				// status, so an error there means the command never ran.
				if config.Comm.Type != "winrm" {
					err := fmt.Errorf("failed to send shutdown command: %s", err)
					state.Put("error", err)
					ui.Error(err.Error())
					return multistep.ActionHalt
				}
				commandErr = err
				ui.Error(fmt.Sprintf("Shutdown command returned an error, waiting to see if the VM stops anyway: %s", err))
			}

		} else {
			ui.Say("Halting the virtual machine...")
			if err := powerOffOrAlreadyOff(ctx, driver, vmUUID); err != nil {
				err := fmt.Errorf("error stopping VM: %s", err)
				state.Put("error", err)
				ui.Error(err.Error())
				return multistep.ActionHalt
			}
		}
	} else {
		ui.Say("Automatic instance stop disabled. Please stop instance manually.")
	}

	// Wait for the machine to actually shut down
	log.Printf("waiting max %s for shutdown to complete", s.Timeout)
	shutdownTimer := time.After(s.Timeout)
	pollInterval := s.pollInterval
	if pollInterval == 0 {
		pollInterval = defaultShutdownPollInterval
	}
	// lastGetVMErr holds the most recent poll's GetVM error, and is reported if
	// the wait times out, so a persistent error (e.g. 401 or 404) is not hidden
	// behind a bare timeout. A successful poll clears it.
	var lastGetVMErr error
	for {
		// GetVM honours ctx, so it errors once the build is cancelled; a nil
		// VM must not be dereferenced.
		running, err := driver.GetVM(ctx, vmUUID)
		lastGetVMErr = err
		if err != nil {
			log.Printf("error getting VM power state: %s", err)
		} else if running.PowerState() == "OFF" {
			log.Printf("VM powered off")
			break
		}

		select {
		case <-ctx.Done():
			err := fmt.Errorf("build cancelled while waiting for machine to shutdown: %w", ctx.Err())
			state.Put("error", err)
			ui.Error(err.Error())
			return multistep.ActionHalt
		case <-shutdownTimer:
			err := errors.New("timeout while waiting for machine to shutdown")
			if commandErr != nil {
				err = fmt.Errorf("%w; the shutdown command failed: %w", err, commandErr)
			}
			if lastGetVMErr != nil {
				err = fmt.Errorf("%w; last error getting VM power state: %w", err, lastGetVMErr)
			}
			state.Put("error", err)
			ui.Error(err.Error())
			return multistep.ActionHalt
		case <-time.After(pollInterval):
		}
	}

	log.Println("VM shut down.")

	return multistep.ActionContinue
}

// powerOffOrAlreadyOff powers the VM off. A PowerOff error is ignored only
// when the VM is confirmed to be off already; otherwise it is returned.
func powerOffOrAlreadyOff(ctx context.Context, driver Driver, vmUUID string) error {
	err := driver.PowerOff(ctx, vmUUID)
	if err == nil {
		return nil
	}
	if vm, getErr := driver.GetVM(ctx, vmUUID); getErr == nil && vm.PowerState() == "OFF" {
		log.Printf("PowerOff returned an error but the VM is already off: %s", err)
		return nil
	}
	return err
}

func (s *StepShutdown) Cleanup(state multistep.StateBag) {}
