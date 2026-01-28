package emergency_test

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/emergency"
)

// Example demonstrates emergency controller usage
func Example() {
	// Create emergency controller
	controller := emergency.New(emergency.Config{
		StopFile:             "/tmp/chaos-emergency-stop-test",
		PollInterval:         1 * time.Second,
		EnableSignalHandlers: false, // Disable signal handling in example
	})

	// Clean up stop file before starting
	os.Remove(controller.GetStopFilePath())

	// Register cleanup callback
	controller.OnStop(func() {
		fmt.Println("Emergency stop triggered!")
		fmt.Println("Cleaning up resources...")
		fmt.Println("Cleanup complete")
	})

	// Start monitoring
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	controller.Start(ctx)

	// Simulate work
	fmt.Println("Controller started, monitoring for emergency stop...")
	fmt.Println("Create stop file to trigger emergency stop:")
	fmt.Printf("  touch %s\n", controller.GetStopFilePath())

	// Wait for emergency stop or timeout
	select {
	case <-controller.StopChannel():
		fmt.Println("Emergency stop detected via channel")
	case <-time.After(3 * time.Second):
		fmt.Println("No emergency stop triggered (timeout)")
	}

	// Clean up stop file
	os.Remove(controller.GetStopFilePath())

	// Output:
	// Controller started, monitoring for emergency stop...
	// Create stop file to trigger emergency stop:
	//   touch /tmp/chaos-emergency-stop-test
	// No emergency stop triggered (timeout)
}
