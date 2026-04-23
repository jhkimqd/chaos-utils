package container

import "time"

// RestartParams defines parameters for container restart fault
type RestartParams struct {
	// GracePeriod is the number of seconds to wait before force-killing the container
	GracePeriod int `yaml:"grace_period,omitempty"`

	// RestartDelay is the number of seconds to wait after stop before restart
	RestartDelay int `yaml:"restart_delay,omitempty"`

	// Stagger is the number of seconds between restarts when multiple targets
	Stagger int `yaml:"stagger,omitempty"`
}

// KillParams defines parameters for container kill fault
type KillParams struct {
	// Signal is the signal to send (SIGKILL, SIGTERM, SIGHUP, etc.)
	Signal string `yaml:"signal,omitempty"`

	// Restart indicates whether to restart the container after killing
	Restart bool `yaml:"restart,omitempty"`

	// RestartDelay is the number of seconds to wait before restarting
	RestartDelay int `yaml:"restart_delay,omitempty"`
}

// PauseParams defines parameters for container pause fault
type PauseParams struct {
	// Duration is how long to pause the container
	Duration time.Duration `yaml:"duration,omitempty"`

	// Unpause indicates whether to automatically unpause after duration
	Unpause bool `yaml:"unpause,omitempty"`
}
