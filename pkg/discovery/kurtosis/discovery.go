package kurtosis

import (
	"context"
	"fmt"
	"regexp"

	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/enclaves"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"github.com/jihwankim/chaos-utils/pkg/discovery"
	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
)

// Discovery provides Kurtosis-based service discovery
type Discovery struct {
	kurtosisCtx  *kurtosis_context.KurtosisContext
	dockerClient *docker.Client
}

// New creates a new Kurtosis discovery client
func New(dockerClient *docker.Client) (*Discovery, error) {
	// Create Kurtosis context
	ctx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		return nil, fmt.Errorf("failed to create Kurtosis context: %w", err)
	}

	return &Discovery{
		kurtosisCtx:  ctx,
		dockerClient: dockerClient,
	}, nil
}

// Close closes the discovery client
func (d *Discovery) Close() error {
	// Kurtosis context doesn't need explicit cleanup
	return nil
}

// DiscoverServices discovers all services in a Kurtosis enclave
func (d *Discovery) DiscoverServices(ctx context.Context, enclaveName string) ([]*discovery.Service, error) {
	// Get enclave context
	enclaveCtx, err := d.kurtosisCtx.GetEnclaveContext(ctx, enclaveName)
	if err != nil {
		return nil, fmt.Errorf("failed to get enclave context: %w", err)
	}

	// Get all services
	services, err := enclaveCtx.GetServices()
	if err != nil {
		return nil, fmt.Errorf("failed to get services: %w", err)
	}

	// Convert to our Service type
	result := make([]*discovery.Service, 0, len(services))
	for serviceName, serviceCtx := range services {
		svc := &discovery.Service{
			Name:      serviceName,
			ShortName: serviceName,
			IP:        serviceCtx.GetPrivateIPAddress().String(),
			Ports:     make(map[string]uint16),
		}

		// Extract ports
		ports := serviceCtx.GetPrivatePorts()
		for portName, portSpec := range ports {
			svc.Ports[portName] = portSpec.GetNumber()
		}

		// Infer service type and role from name
		svc.Type, svc.Role = inferServiceInfo(serviceName)

		// Try to get container ID from Docker
		// Kurtosis services typically have container names matching service names
		if dockerSvc, err := d.dockerClient.GetContainerByName(ctx, serviceName); err == nil {
			svc.ContainerID = dockerSvc.ContainerID
			svc.ContainerName = dockerSvc.ContainerName
			svc.NetworkMode = dockerSvc.NetworkMode
			svc.PID = dockerSvc.PID
			svc.Labels = dockerSvc.Labels
		} else {
			// Try with kurtosis enclave prefix
			containerName := fmt.Sprintf("%s--%s", enclaveName, serviceName)
			if dockerSvc, err := d.dockerClient.GetContainerByName(ctx, containerName); err == nil {
				svc.ContainerID = dockerSvc.ContainerID
				svc.ContainerName = dockerSvc.ContainerName
				svc.NetworkMode = dockerSvc.NetworkMode
				svc.PID = dockerSvc.PID
				svc.Labels = dockerSvc.Labels
			}
		}

		result = append(result, svc)
	}

	return result, nil
}

// DiscoverServicesByPattern discovers services matching a regex pattern
func (d *Discovery) DiscoverServicesByPattern(ctx context.Context, enclaveName, pattern string) ([]*discovery.Service, error) {
	// Compile regex
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	// Get all services
	allServices, err := d.DiscoverServices(ctx, enclaveName)
	if err != nil {
		return nil, err
	}

	// Filter by pattern
	result := make([]*discovery.Service, 0)
	for _, svc := range allServices {
		if re.MatchString(svc.Name) {
			result = append(result, svc)
		}
	}

	return result, nil
}

// DiscoverServiceByName discovers a specific service by name
func (d *Discovery) DiscoverServiceByName(ctx context.Context, enclaveName, serviceName string) (*discovery.Service, error) {
	services, err := d.DiscoverServices(ctx, enclaveName)
	if err != nil {
		return nil, err
	}

	for _, svc := range services {
		if svc.Name == serviceName {
			return svc, nil
		}
	}

	return nil, fmt.Errorf("service not found: %s", serviceName)
}

// ListEnclaves lists all available Kurtosis enclaves
func (d *Discovery) ListEnclaves(ctx context.Context) ([]string, error) {
	enclaves, err := d.kurtosisCtx.GetEnclaves(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list enclaves: %w", err)
	}

	names := make([]string, 0, len(enclaves))
	for name := range enclaves {
		names = append(names, string(name))
	}

	return names, nil
}

// inferServiceInfo infers service type and role from Kurtosis service name
// Examples:
// - "l2-cl-1-heimdall-v2-validator" -> type: "validator", role: "l2-cl"
// - "l1-el-1-geth-lighthouse" -> type: "rpc", role: "l1"
// - "rabbitmq" -> type: "messaging", role: "messaging"
func inferServiceInfo(serviceName string) (serviceType, role string) {
	// Default values
	serviceType = "unknown"
	role = "unknown"

	// Common patterns
	if regexp.MustCompile(`validator`).MatchString(serviceName) {
		serviceType = "validator"
	} else if regexp.MustCompile(`rpc|geth|bor`).MatchString(serviceName) {
		serviceType = "rpc"
	} else if regexp.MustCompile(`rabbitmq`).MatchString(serviceName) {
		serviceType = "messaging"
	} else if regexp.MustCompile(`heimdall`).MatchString(serviceName) {
		serviceType = "consensus"
	}

	// Infer role from name prefix
	if regexp.MustCompile(`^l1-`).MatchString(serviceName) {
		role = "l1"
	} else if regexp.MustCompile(`^l2-cl-`).MatchString(serviceName) {
		role = "l2-cl"
	} else if regexp.MustCompile(`^l2-el-`).MatchString(serviceName) {
		role = "l2-el"
	} else if regexp.MustCompile(`^rabbitmq`).MatchString(serviceName) {
		role = "messaging"
	}

	return serviceType, role
}
