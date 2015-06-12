// The openstack package contains a packer.Builder implementation that
// builds Images for openstack.

package openstack

import (
	"fmt"
	"github.com/mitchellh/multistep"
	"github.com/mitchellh/packer/common"
	"log"

	"github.com/mitchellh/packer/helper/config"
	"github.com/mitchellh/packer/packer"
	"github.com/mitchellh/packer/template/interpolate"
)

// The unique ID for this builder
const BuilderId = "mitchellh.openstack"

type Config struct {
	common.PackerConfig `mapstructure:",squash"`
	AccessConfig        `mapstructure:",squash"`
	ImageConfig         `mapstructure:",squash"`
	RunConfig           `mapstructure:",squash"`

	ctx interpolate.Context
}

type Builder struct {
	config Config
	runner multistep.Runner
}

func (b *Builder) Prepare(raws ...interface{}) ([]string, error) {
	err := config.Decode(&b.config, &config.DecodeOpts{
		Interpolate: true,
	}, raws...)
	if err != nil {
		return nil, err
	}

	// Accumulate any errors
	var errs *packer.MultiError
	errs = packer.MultiErrorAppend(errs, b.config.AccessConfig.Prepare(&b.config.ctx)...)
	errs = packer.MultiErrorAppend(errs, b.config.ImageConfig.Prepare(&b.config.ctx)...)
	errs = packer.MultiErrorAppend(errs, b.config.RunConfig.Prepare(&b.config.ctx)...)

	if errs != nil && len(errs.Errors) > 0 {
		return nil, errs
	}

	log.Println(common.ScrubConfig(b.config, b.config.Password))
	return nil, nil
}

func (b *Builder) Run(ui packer.Ui, hook packer.Hook, cache packer.Cache) (packer.Artifact, error) {
	computeClient, err := b.config.computeV2Client()
	if err != nil {
		return nil, fmt.Errorf("Error initializing compute client: %s", err)
	}

	// Setup the state bag and initial state for the steps
	state := new(multistep.BasicStateBag)
	state.Put("config", b.config)
	state.Put("hook", hook)
	state.Put("ui", ui)

	// Build the steps
	steps := []multistep.Step{
		&StepLoadFlavor{
			Flavor: b.config.Flavor,
		},
		&StepKeyPair{
			Debug:        b.config.PackerDebug,
			DebugKeyPath: fmt.Sprintf("os_%s.pem", b.config.PackerBuildName),
		},
		&StepRunSourceServer{
			Name:             b.config.ImageName,
			SourceImage:      b.config.SourceImage,
			SecurityGroups:   b.config.SecurityGroups,
			Networks:         b.config.Networks,
			AvailabilityZone: b.config.AvailabilityZone,
		},
		&StepWaitForRackConnect{
			Wait: b.config.RackconnectWait,
		},
		&StepAllocateIp{
			FloatingIpPool: b.config.FloatingIpPool,
			FloatingIp:     b.config.FloatingIp,
		},
		&common.StepConnectSSH{
			SSHAddress:     SSHAddress(computeClient, b.config.SSHInterface, b.config.SSHPort),
			SSHConfig:      SSHConfig(b.config.SSHUsername),
			SSHWaitTimeout: b.config.SSHTimeout(),
		},
		&common.StepProvision{},
		&stepCreateImage{},
	}

	// Run!
	if b.config.PackerDebug {
		b.runner = &multistep.DebugRunner{
			Steps:   steps,
			PauseFn: common.MultistepDebugFn(ui),
		}
	} else {
		b.runner = &multistep.BasicRunner{Steps: steps}
	}

	b.runner.Run(state)

	// If there was an error, return that
	if rawErr, ok := state.GetOk("error"); ok {
		return nil, rawErr.(error)
	}

	// If there are no images, then just return
	if _, ok := state.GetOk("image"); !ok {
		return nil, nil
	}

	// Build the artifact and return it
	artifact := &Artifact{
		ImageId:        state.Get("image").(string),
		BuilderIdValue: BuilderId,
		Client:         computeClient,
	}

	return artifact, nil
}

func (b *Builder) Cancel() {
	if b.runner != nil {
		log.Println("Cancelling the step runner...")
		b.runner.Cancel()
	}
}
