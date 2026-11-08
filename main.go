// ctop is a terminal UI for Docker containers, in the spirit of htop/btop.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	flag "github.com/spf13/pflag"

	"github.com/Arctize/ctop/internal/docker"
	"github.com/Arctize/ctop/internal/ui"
)

var version = "dev"

func main() {
	var (
		host        = flag.StringP("host", "H", "", "docker host: name (→ ssh://name), user@host:port, ssh://, tcp://, unix://")
		dctx        = flag.StringP("context", "c", "", "docker context to use")
		interval    = flag.DurationP("interval", "i", 2*time.Second, "refresh interval")
		running     = flag.BoolP("running", "r", false, "show only running containers on start")
		noGroup     = flag.Bool("no-group", false, "don't group containers by compose project")
		tail        = flag.Int("tail", 1000, "number of log lines to load when opening logs")
		runtime     = flag.String("runtime", "auto", "container engine: auto, docker or podman (ssh: remote CLI; local: socket lookup)")
		noMux       = flag.Bool("no-ssh-mux", false, "don't share one ssh connection (ControlMaster) for all requests")
		noMouse     = flag.Bool("no-mouse", false, "disable mouse support")
		showVersion = flag.BoolP("version", "v", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "ctop — a top-like TUI for docker/podman containers\n\nUsage: ctop [flags]\n\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n  ctop                 local daemon (DOCKER_HOST / current context)\n  ctop -H node         docker on 'node' via ssh (uses ~/.ssh/config)\n  ctop -H me@box:2222  ssh with user and port\n  ctop -H tcp://10.0.0.5:2375\n  ctop -H box --runtime podman   podman on a remote host (needs podman.socket enabled there)\n")
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("ctop", version)
		return
	}

	spec, err := docker.Resolve(*host, *dctx, *runtime)
	if err != nil {
		fatal(err)
	}
	cli, err := docker.New(spec, docker.Options{NoSSHMux: *noMux, Runtime: *runtime})
	if err != nil {
		fatal(err)
	}
	defer cli.Close()

	// Connect before entering the alternate screen so ssh can prompt for
	// passwords / host key confirmation on the terminal.
	if cli.IsSSH() {
		fmt.Fprintf(os.Stderr, "connecting to %s…\r", spec.Host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	err = cli.Ping(ctx)
	cancel()
	if cli.IsSSH() {
		fmt.Fprintf(os.Stderr, "\x1b[2K\r")
	}
	if err != nil {
		fatal(fmt.Errorf("cannot connect to docker at %s: %w", spec.Host, err))
	}
	cli.Detach()

	m := ui.New(cli, ui.Config{
		Interval: *interval,
		ShowAll:  !*running,
		Group:    !*noGroup,
		Tail:     *tail,
	})
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if !*noMouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ctop:", err)
	os.Exit(1)
}
