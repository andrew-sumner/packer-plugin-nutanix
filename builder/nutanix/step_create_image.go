package nutanix

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
	vmmModels "github.com/nutanix/ntnx-api-golang-clients/vmm-go-client/v4/models/vmm/v4/ahv/config"
)

// cleanupTimeout bounds each step cleanup operation. Cleanup is detached from
// build cancellation so its delete requests are still sent after a cancel, so
// without this a stuck delete task would block packer from exiting.
const cleanupTimeout = 10 * time.Minute

// withCleanupTimeout runs one cleanup operation under its own cleanupTimeout,
// so a slow operation cannot use up the budget of the ones after it.
func withCleanupTimeout(ctx context.Context, op func(context.Context) error) error {
	opCtx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	return op(opCtx)
}

type imageArtefact struct {
	uuid string
	size int64
}

type diskArtefact struct {
	uuid string
	size int64
}

type stepCreateImage struct {
	Config *Config
}

func (s *stepCreateImage) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)
	vmUUID := state.Get("vm_uuid").(string)
	d := state.Get("driver").(Driver)
	vm, _ := d.GetVM(ctx, vmUUID)

	ui.Say(fmt.Sprintf("Creating image(s) from virtual machine %s...", s.Config.VMName))

	// Choose disk to replicate - looking for SCSI disks (data disks, not CDROMs)
	// In V4, CDROMs use SATA bus while data disks use SCSI
	var disksToCopy []diskArtefact

	for _, disk := range vm.Disks() {
		// Only process SCSI disks (data disks)
		if disk.DiskAddress != nil && disk.DiskAddress.BusType != nil {
			if disk.DiskAddress.BusType.GetName() == vmmModels.DISKBUSTYPE_SCSI.GetName() {
				diskUUID := ""
				if disk.ExtId != nil {
					diskUUID = *disk.ExtId
				}

				// Get disk size from backing info if available
				var diskSize int64 = 0
				if disk.BackingInfo != nil {
					if backingValue := disk.BackingInfo.GetValue(); backingValue != nil {
						if vmDiskInfo, ok := backingValue.(vmmModels.VmDisk); ok && vmDiskInfo.DiskSizeBytes != nil {
							diskSize = *vmDiskInfo.DiskSizeBytes
						}
					}
				}

				disksToCopy = append(disksToCopy, diskArtefact{
					uuid: diskUUID,
					size: diskSize,
				})

				diskIndex := 0
				if disk.DiskAddress.Index != nil {
					diskIndex = *disk.DiskAddress.Index
				}
				diskID := fmt.Sprintf("SCSI:%d", diskIndex)
				ui.Say("Found disk to copy: " + diskID)
			}
		}
	}

	if len(disksToCopy) == 0 {
		err := errors.New("no DISK was found to save, halting build")
		ui.Error(err.Error())
		state.Put("error", err)
		return multistep.ActionHalt
	}

	var imageList []imageArtefact

	for i, diskToCopy := range disksToCopy {

		imageResponse, err := d.SaveVMDisk(ctx, diskToCopy.uuid, i, s.Config.ImageCategories)
		if err != nil {
			ui.Error("Image creation failed: " + err.Error())
			state.Put("error", err)
			return multistep.ActionHalt
		}

		imageList = append(imageList, imageArtefact{
			uuid: imageResponse.UUID(),
			size: diskToCopy.size,
		})

		ui.Say(fmt.Sprintf("Image successfully created: %s (%s)", imageResponse.Name(), imageResponse.UUID()))
	}

	state.Put("image_uuid", imageList)
	return multistep.ActionContinue
}

func (s *stepCreateImage) Cleanup(state multistep.StateBag) {
	ui := state.Get("ui").(packer.Ui)
	d := state.Get("driver").(Driver)
	ctx, ok := state.Get("ctx").(context.Context)
	if !ok {
		ctx = context.Background()
	}
	// Cleanup also runs after the build is cancelled; detach from that
	// cancellation so the delete request is still sent.
	ctx = context.WithoutCancel(ctx)

	if !s.Config.ImageDelete {
		return
	}

	if imgUUID, ok := state.GetOk("image_uuid"); ok {
		ui.Say(fmt.Sprintf("Deleting image(s) %s...", s.Config.ImageName))

		for _, image := range imgUUID.([]imageArtefact) {

			err := withCleanupTimeout(ctx, func(ctx context.Context) error {
				return d.DeleteImage(ctx, image.uuid)
			})
			if err != nil {
				ui.Error("An error occurred while deleting image")
				return
			} else {
				ui.Say(fmt.Sprintf("Image successfully deleted (%s)", image.uuid))
			}
		}
	}
}
