package commands

import (
	"log"
	"os"
	"syscall"
	"time"
)

// restartGraceperiod gives an in-flight command response time to leave the
// wire before the process exits and systemd respawns it.
const restartGracePeriod = 500 * time.Millisecond

// restart signals the process to terminate after a short grace period. The
// systemd unit is configured with Restart=always, so the service comes back
// up and reloads its on-disk config.
func (h *Handler) restart() {
	log.Printf("[CommandHandler] Restart requested; exiting in %s for systemd respawn", restartGracePeriod)
	go func() {
		time.Sleep(restartGracePeriod)
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			log.Printf("[CommandHandler] Failed to signal self for restart: %v", err)
		}
	}()
}
