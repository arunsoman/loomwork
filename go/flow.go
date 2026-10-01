package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"loomwork.dev/loomwork/internal/agents"
	"loomwork.dev/loomwork/internal/flow"
)

// cmdFlow coordinates several agent CLIs on one goal.
//
//	loomwork flow run "goal" [--workflow name] [--yes] [--push]
//	loomwork flow status [--watch]
//	loomwork flow workflows
func cmdFlow(args []string) {
	if len(args) == 0 {
		fail(fmt.Errorf("usage: loomwork flow run|status|workflows"))
	}
	switch args[0] {
	case "run":
		cmdFlowRun(args[1:])
	case "status":
		cmdFlowStatus(args[1:])
	case "context":
		cmdFlowContext(args[1:])
	case "workflows":
		folder, _ := os.Getwd()
		for _, n := range flow.ListWorkflows(folder) {
			fmt.Println(n)
		}
	default:
		fail(fmt.Errorf("unknown flow command %q", args[0]))
	}
}

func knownAgents(cfg *agents.Config) map[string]bool {
	m := map[string]bool{}
	for _, a := range cfg.Agents {
		m[a.Name] = true
	}
	return m
}

func cmdFlowRun(args []string) {
	fs := flag.NewFlagSet("flow run", flag.ExitOnError)
	name := fs.String("workflow", "default", "workflow name (.agent/workflows/<name>.json)")
	yes := fs.Bool("yes", false, "approve the plan without asking")
	push := fs.Bool("push", false, "allow the ship stage to push (still asks to confirm)")
	folder := fs.String("folder", ".", "project folder (a git repository)")
	ctxAgent := fs.String("context-agent", "", "agent that acts as the context manager for this run (overrides the workflow's)")
	goal := strings.Join(parseArgs(fs, args), " ")
	if goal == "" {
		fail(fmt.Errorf("usage: loomwork flow run \"goal\" [--workflow name] [--yes] [--push]"))
	}
	abs, err := filepath.Abs(*folder)
	if err != nil {
		fail(err)
	}
	cfg, err := agents.LoadConfig(loomHome())
	if err != nil {
		fail(err)
	}
	w, err := flow.LoadWorkflow(abs, *name)
	if err != nil {
		fail(err)
	}
	if *ctxAgent != "" {
		w.ContextAgent = *ctxAgent
	}
	if err := w.Validate(knownAgents(cfg)); err != nil {
		fail(fmt.Errorf("workflow %s: %w", *name, err))
	}
	// Every agent used here sends prompts to its provider under its own login.
	fmt.Fprintf(os.Stderr, "Workflow %q uses cloud agent CLIs: %s\n", w.Name, workflowAgents(w))
	e, err := flow.NewEngine(abs, w, cfg)
	if err != nil {
		fail(err)
	}
	if e.Context, err = flow.LoadContext(abs); err != nil {
		fail(err)
	}
	in := bufio.NewReader(os.Stdin)
	ask := func(q string) bool {
		fmt.Fprintf(os.Stderr, "%s [y/N] ", q)
		line, _ := in.ReadString('\n')
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
	}
	e.Logf = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "  "+f+"\n", a...) }
	e.ApprovePlan = func(ms []flow.Module) bool {
		for _, m := range ms {
			fmt.Fprintf(os.Stderr, "  • %s — %s (after: %s) owns %s\n", m.ID, m.Title, strings.Join(m.DependsOn, ","), strings.Join(m.Paths, ","))
		}
		return *yes || ask("Build this plan?")
	}
	if *push {
		e.ConfirmPush = func(b string) bool { return ask("Push branch " + b + " to origin?") }
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := e.Run(ctx, goal)
	if res != nil {
		fmt.Printf("branch: %s\nmerged: %s\nfailed: %s\npushed: %v\n", res.Branch, strings.Join(res.Merged, ","), strings.Join(res.Failed, ","), res.Pushed)
		if res.Note != "" {
			fmt.Println(res.Note)
		}
	}
	if err != nil {
		fail(err)
	}
}

func workflowAgents(w *flow.Workflow) string {
	seen := map[string]bool{}
	var out []string
	if w.ContextAgent != "" {
		seen[w.ContextAgent] = true
		out = append(out, w.ContextAgent+" (context manager)")
	}
	for _, s := range w.Stages {
		for _, a := range s.Agents {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return strings.Join(out, ", ")
}

func cmdFlowStatus(args []string) {
	fs := flag.NewFlagSet("flow status", flag.ExitOnError)
	watch := fs.Bool("watch", false, "refresh every second")
	folder := fs.String("folder", ".", "project folder")
	parseArgs(fs, args)
	b, err := flow.OpenBoard(*folder)
	if err != nil {
		fail(err)
	}
	for {
		tasks, err := b.List()
		if err != nil {
			fail(err)
		}
		if *watch {
			fmt.Print("\033[H\033[2J")
		}
		fmt.Print(flow.Summary(tasks))
		if !*watch {
			return
		}
		time.Sleep(time.Second)
	}
}

// cmdFlowContext shows or scaffolds the context policy that controls what
// each agent can see.
//
//	loomwork flow context show [role agent]   limits the manager works within
//	loomwork flow context last                what the manager actually sent each agent
//	loomwork flow context init
func cmdFlowContext(args []string) {
	folder, _ := os.Getwd()
	pol, err := flow.LoadContext(folder)
	if err != nil {
		fail(err)
	}
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "init":
		if _, err := os.Stat(flow.ContextPath(folder)); err == nil {
			fail(fmt.Errorf("%s already exists", flow.ContextPath(folder)))
		}
		if err := flow.SaveContext(folder, flow.DefaultContext()); err != nil {
			fail(err)
		}
		fmt.Println("wrote", flow.ContextPath(folder))
	case "last":
		for _, bn := range flow.RecentBundles(folder, 20) {
			fmt.Printf("%s %s/%s task=%s sent %d/%d bytes, %d files hidden\n", bn.Time.Local().Format("15:04:05"), bn.Role, bn.Agent, bn.Task, bn.Used, bn.Budget, bn.Hidden)
			for _, it := range bn.Items {
				fmt.Printf("    %-12s %-28s %6dB  %s\n", it.Kind, it.Name, it.Bytes, it.Why)
			}
			for _, d := range bn.Dropped {
				fmt.Printf("    dropped: %s\n", d)
			}
			if bn.Curator != "" {
				fmt.Printf("    context manager: %s; briefing: %q\n", bn.Curator, bn.Briefing)
			}
			for _, a := range bn.Anomalies {
				fmt.Printf("    discarded from manager reply: %s\n", a)
			}
		}
	case "show":
		roles := []string{flow.KindPlan, flow.KindBuild, flow.KindVerify, flow.KindShip}
		if len(args) >= 3 {
			roles = []string{args[1]}
		}
		agent := ""
		if len(args) >= 3 {
			agent = args[2]
		}
		for _, role := range roles {
			r := pol.Rule(role, agent)
			fmt.Printf("%-7s may receive: %s\n        allow: %s\n        hidden: %s\n        cap:   %d bytes\n",
				role, strings.Join(r.Include, ", "), orAll(r.Allow), strings.Join(r.Deny, " "), r.MaxBytes)
		}
	default:
		fail(fmt.Errorf("usage: loomwork flow context show|last|init"))
	}
}

func orAll(s []string) string {
	if len(s) == 0 {
		return "(everything not denied)"
	}
	return strings.Join(s, " ")
}
