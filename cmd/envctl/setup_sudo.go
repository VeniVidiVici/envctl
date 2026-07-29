package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/VeniVidiVici/envctl/internal/setupui"
)

const automaticSudoRefreshInterval = 30 * time.Second

func automaticSetupNeedsSudo(phases []setupui.Phase) bool {
	for _, phase := range phases {
		if phase.ID == setupui.PhaseHomebrew {
			return phase.Status == setupui.StatusReady && phase.Actions > 0
		}
	}
	return false
}

func authorizeAutomaticSetup(
	ctx context.Context,
	stdout, stderr io.Writer,
	refreshInterval time.Duration,
) (func(), error) {
	fmt.Fprintln(
		stdout,
		"Automatic setup may run privileged macOS package installers.",
	)
	fmt.Fprintln(
		stdout,
		"Enter your Mac password once before the setup interface starts.",
	)

	command := exec.CommandContext(ctx, "sudo", "-v")
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("authorize automatic package setup: %w", err)
	}

	keeperContext, cancelKeeper := context.WithCancel(ctx)
	keeperDone := make(chan struct{})
	go func() {
		defer close(keeperDone)
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-keeperContext.Done():
				return
			case <-ticker.C:
				refresh := exec.CommandContext(
					keeperContext,
					"sudo",
					"-n",
					"-v",
				)
				refresh.Stdin = os.Stdin
				refresh.Stdout = io.Discard
				refresh.Stderr = io.Discard
				_ = refresh.Run()
			}
		}
	}()

	stop := func() {
		cancelKeeper()
		<-keeperDone
	}
	return stop, nil
}
