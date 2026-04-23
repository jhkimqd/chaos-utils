package discovery

// Service represents a discovered service in the Kurtosis enclave
type Service struct {
	// Name is the Kurtosis service name
	Name string

	// ShortName is a user-friendly short name
	ShortName string

	// Type indicates the service type (validator, rpc, rabbitmq, etc.)
	Type string

	// Role indicates the role (l1, l2-cl, l2-el, messaging, etc.)
	Role string

	// IP is the service IP address
	IP string

	// Ports maps port names to port numbers
	Ports map[string]uint16

	// ContainerID is the Docker container ID
	ContainerID string

	// ContainerName is the Docker container name
	ContainerName string

	// NetworkMode is the Docker network mode
	NetworkMode string

	// PID is the container process ID (for nsenter)
	PID int

	// Labels are Docker container labels
	Labels map[string]string
}

// ServiceFilter defines criteria for filtering services
type ServiceFilter struct {
	// NamePattern is a regex pattern for service name matching
	NamePattern string

	// Type filters by service type
	Type string

	// Role filters by service role
	Role string

	// Labels filters by Docker labels
	Labels map[string]string
}
