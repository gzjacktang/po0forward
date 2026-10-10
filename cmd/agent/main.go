package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gzjacktang/po0forward/internal/agent"
	"github.com/gzjacktang/po0forward/internal/model"
	"github.com/gzjacktang/po0forward/internal/storage"
)

func main() {
	mode := "run"
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "apply" || args[0] == "render" || args[0] == "validate") {
		mode = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("agent", flag.ExitOnError)
	path := flags.String("config", "/etc/po0forward/agent.json", "config file")
	snapshotPath := flags.String("snapshot", "", "snapshot file for apply/render")
	_ = flags.Parse(args)
	var c agent.Config
	if err := storage.ReadJSON(*path, &c); err != nil {
		log.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		log.Fatal(err)
	}
	if mode == "validate" {
		fmt.Println("节点配置检查通过")
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if mode != "run" {
		var s model.Snapshot
		if err := storage.ReadJSON(*snapshotPath, &s); err != nil {
			log.Fatal(err)
		}
		if mode == "render" {
			if s.NodeID != c.NodeID {
				log.Fatal("节点 ID 不匹配")
			}
			if err := model.ValidateSnapshot(s, c.ProtectedPorts); err != nil {
				log.Fatal(err)
			}
			text, err := agent.Render(c.TableName, s, false)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Print(text)
			return
		}
		if err := agent.NewEngine(c).Apply(ctx, s); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := agent.Run(ctx, c); err != nil {
		log.Fatal(err)
	}
}
