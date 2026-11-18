// Command benchwatch serves the monitor API on 127.0.0.1:8790 and runs the collectors, the transcript
// scanner, the guard copy and the history upkeep.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"benchwatch/server/internal/codelog"
	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/docker"
	"benchwatch/server/internal/collect/machine"
	"benchwatch/server/internal/collect/runner"
	"benchwatch/server/internal/guard"
	"benchwatch/server/internal/guardlog"
	"benchwatch/server/internal/history"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/server"
	"benchwatch/server/internal/source"
	"benchwatch/server/internal/source/codex"
	"benchwatch/server/internal/source/copilot"
	"benchwatch/server/internal/source/gemini"
	"benchwatch/server/internal/source/opencode"
	"benchwatch/server/internal/store"
	"benchwatch/server/internal/transcripts"
)

const Addr = "127.0.0.1:8790"

const dockerAuto = "auto"

type options struct {
	passfile, cache, projects, dockerHost, home string
}

func defaults() options {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, "Library", "Application Support", "benchwatch", "server")
	return options{passfile: filepath.Join(base, "pgpass"), cache: filepath.Join(base, "cache"),
		projects:   filepath.Join(home, ".claude", "projects"),
		dockerHost: dockerAuto, home: home}
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	o := defaults()
	set := flag.NewFlagSet("benchwatch", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&o.passfile, "passfile", o.passfile, "PostgreSQL password file of the benchwatch role")
	set.StringVar(&o.cache, "cache", o.cache, "private cache folder")
	set.StringVar(&o.projects, "projects", o.projects, "Claude Code transcripts folder")
	set.StringVar(&o.dockerHost, "docker", o.dockerHost, "Docker socket URL; \"auto\" finds it from DOCKER_HOST or the platform defaults; empty to skip Docker")
	if err := set.Parse(os.Args[1:]); err != nil || set.NArg() > 0 {
		log.Print("start: EUSAGE")
		os.Exit(2)
	}
	if err := run(o); err != nil {
		log.Printf("exit: %s", codelog.Clean(transcripts.Code(err)))
		os.Exit(1)
	}
}

// readers is Claude Code's reader plus each other client's reader that detects records at start; a client installed
// later is read after the next restart.
func readers(projects string, others ...source.Reader) []source.Reader {
	out := []source.Reader{scan.NewClaudeReader(projects)}
	for _, r := range others {
		if r.Detect() {
			out = append(out, r)
		}
	}
	return out
}

func code(err error) string { return codelog.Clean(transcripts.Code(err)) }

// dsn names only the passfile; the password never passes through flags, environment or logs.
func dsn(passfile string) string {
	return fmt.Sprintf("host=127.0.0.1 port=5432 dbname=benchwatch user=benchwatch sslmode=require "+
		"connect_timeout=5 passfile='%s' pool_max_conns=6 application_name=benchwatch", passfile)
}

func intervals(ctx context.Context, pool *pgxpool.Pool) (hist, apps time.Duration, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s, err := store.New(pool).CollectorSettings(ctx)
	if err != nil {
		return 0, 0, err
	}
	return time.Duration(s.DetailIntervalSeconds) * time.Second, time.Duration(s.TopAppsIntervalSeconds) * time.Second,
		nil
}

// errSettings ends the process when the stored intervals differ from those in use, so launchd starts it with them.
var errSettings = codedError("ESETTINGS")

type codedError string

func (e codedError) Error() string { return string(e) }
func (e codedError) Code() string  { return string(e) }

// watchSettings rereads the intervals every `every`, which also recovers a start when PostgreSQL was down.
func watchSettings(ctx context.Context, read func(context.Context) (time.Duration, time.Duration, error),
	o runner.Options, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		hist, apps, err := read(ctx)
		codelog.Note("settings", codelog.Of(err))
		if err != nil {
			continue
		}
		if h, a := runner.Intervals(o.Live, hist, apps); h != o.Hist || a != o.Apps {
			return errSettings
		}
	}
}

// dockerOptions says how to reach Docker: not at all for an empty flag, the given URL as it is, or, for auto, a
// resolver that looks for the socket again on every call. A DOCKER_HOST scheme other than unix is logged and never
// dialled.
func dockerOptions(flagValue string, env docker.Env) (docker.Options, bool) {
	switch flagValue {
	case "":
		return docker.Options{}, false
	case dockerAuto:
		if _, presence := docker.Resolve(env); presence == docker.Unsupported {
			log.Print("docker: EHOSTSCHEME")
		}
		return docker.Options{Resolve: func() (string, docker.Presence) { return docker.Resolve(env) }}, true
	}
	return docker.Options{Host: flagValue}, true
}

// nameGate is the gate of both collectors: a name is refused when the collector's own checks or the label
// gate's LooksSecret, fed word by word, flag it (CM-00009 M1, CM-00010).
func nameGate() collect.NameGate { return collect.DefaultNameGate }

func run(o options) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := os.MkdirAll(o.cache, 0o700); err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(dsn(o.passfile))
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	hist, apps, err := intervals(ctx, pool)
	codelog.Note("settings", codelog.Of(err))
	gate := nameGate()
	status := collect.NewStatus(nil)
	mc := machine.New(machine.NewDarwin(), machine.Options{Gate: gate})
	var dc *docker.Collector
	if opt, wanted := dockerOptions(o.dockerHost, docker.OSEnv()); wanted {
		opt.Gate = gate
		if dc, err = docker.New(opt); err != nil {
			log.Printf("docker: %s", code(err))
			status.Fail(collect.SourceDocker, collect.StateUnavailable, err)
			dc = nil
		}
	}
	written := &history.Written{}
	r := runner.New(runner.Options{Hist: hist, Apps: apps}, mc, dc, history.Sink{DB: pool, Written: written}, status)
	upkeep := &history.Upkeep{DB: pool, Written: written}
	var others []source.Reader
	if dir, err := codex.Home(); err != nil {
		log.Printf("codex: %s", code(err))
	} else {
		others = append(others, codex.New(dir))
	}
	if dir, err := gemini.GeminiHome(); err != nil {
		log.Printf("gemini: %s", code(err))
	} else {
		others = append(others, gemini.NewGemini(dir))
	}
	if dir, err := gemini.QwenHome(); err != nil {
		log.Printf("qwen: %s", code(err))
	} else {
		others = append(others, gemini.NewQwen(dir))
	}
	if dir, err := copilot.Home(); err != nil {
		log.Printf("copilot: %s", code(err))
	} else {
		others = append(others, copilot.New(dir))
	}
	if dir, err := opencode.Home(); err != nil {
		log.Printf("opencode: %s", code(err))
	} else {
		others = append(others, opencode.New(dir))
	}
	sc := transcripts.New(transcripts.Options{Projects: o.projects, Cache: o.cache,
		Readers: readers(o.projects, others...)}, pool)
	g := &transcripts.Guard{Dir: guardlog.Dir(o.home), DB: pool}
	hub, err := server.NewHub()
	if err != nil {
		return err
	}
	srv := &server.Server{DB: pool, Runner: r, Scanner: sc, Guard: g, GuardInstall: guard.NewInstallReader(o.home),
		Upkeep: upkeep, Hub: hub}
	sc.OnActivity = func(a transcripts.Activity) { hub.Activity(server.ActivityItem(a)) }
	handler, err := server.Handler(srv)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp4", Addr)
	if err != nil {
		return err
	}
	// Request contexts derive from ctx, so open event streams end when the server stops. Streams set their own
	// write deadline per write, past WriteTimeout.
	hs := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10, ErrorLog: server.ErrorLog,
		BaseContext: func(net.Listener) context.Context { return ctx }}

	var wg sync.WaitGroup
	for _, f := range []func(context.Context){r.Run, sc.Run, g.Run, upkeep.Run,
		func(ctx context.Context) { srv.Publish(ctx, r.Options().Live) }} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f(ctx)
		}()
	}
	served := make(chan error, 2)
	capped := server.CapListener(ln, server.MaxConns)
	hs.ConnState = capped.Track
	go func() { served <- hs.Serve(capped) }()
	read := func(ctx context.Context) (time.Duration, time.Duration, error) { return intervals(ctx, pool) }
	go func() { served <- watchSettings(ctx, read, r.Options(), time.Minute) }()
	log.Print("start: ok")
	select {
	case <-ctx.Done():
	case err = <-served:
		stop()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdown)
	if !waitAtMost(&wg, 12*time.Second) {
		log.Print("stop: ETIMEDOUT")
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Print("stop: ok")
	return nil
}

// waitAtMost waits for wg until d passes; with the 5 s shutdown it stays inside launchd's 20 s ExitTimeOut.
func waitAtMost(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
