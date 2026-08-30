package commands

import (
	"log"
	"os"
	"syscall"
	"time"
)

const restartGracePeriod = 500 * time.Millisecond

func (h *Handler) restart() {
	log.Printf("[CommandHandler] Restart requested; exiting in %s for systemd respawn", restartGracePeriod)
	go func() {
		time.Sleep(restartGracePeriod)
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			log.Printf("[CommandHandler] Failed to signal self for restart: %v", err)
		}
	}()
}
