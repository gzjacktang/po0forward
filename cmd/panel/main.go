package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/gzjacktang/po0forward/internal/panel"
	"github.com/gzjacktang/po0forward/internal/storage"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "password" {
		flags := flag.NewFlagSet("password", flag.ExitOnError)
		path := flags.String("config", "/etc/po0forward/panel.json", "config file")
		_ = flags.Parse(os.Args[2:])
		var c panel.Config
		if err := storage.ReadJSON(*path, &c); err != nil {
			log.Fatal(err)
		}
		_, password, err := panel.NewConfig(c.PublicURL, c.StateFile)
		if err != nil {
			log.Fatal(err)
		}
		if err = c.SetPassword(password); err != nil {
			log.Fatal(err)
		}
		if err = storage.WriteJSON(*path, c); err != nil {
			log.Fatal(err)
		}
		fmt.Println("New admin password:", password)
		fmt.Println("重启面板以启用新密码并清除已有登录会话。")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "init" {
		flags := flag.NewFlagSet("init", flag.ExitOnError)
		path := flags.String("config", "/etc/po0forward/panel.json", "config file")
		public := flags.String("public-url", "http://127.0.0.1:8090", "external HTTPS URL")
		state := flags.String("state", "/var/lib/po0forward-panel/state.json", "state file")
		_ = flags.Parse(os.Args[2:])
		if _, err := os.Stat(*path); !os.IsNotExist(err) {
			log.Fatal("配置文件已存在或无法检查，拒绝覆盖")
		}
		c, password, err := panel.NewConfig(*public, *state)
		if err != nil {
			log.Fatal(err)
		}
		if err = storage.WriteJSON(*path, c); err != nil {
			log.Fatal(err)
		}
		abs, _ := filepath.Abs(*path)
		fmt.Println("Configuration:", abs)
		fmt.Println("Admin password:", password)
		fmt.Println("密码仅显示这一次；请保存在密码管理器。配置文件不包含明文管理员密码。")
		return
	}
	args := os.Args[1:]
	validateOnly := len(args) > 0 && args[0] == "validate"
	if validateOnly {
		args = args[1:]
	}
	flags := flag.NewFlagSet("panel", flag.ExitOnError)
	config := flags.String("config", "/etc/po0forward/panel.json", "config file")
	_ = flags.Parse(args)
	var c panel.Config
	if err := storage.ReadJSON(*config, &c); err != nil {
		log.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		log.Fatal(err)
	}
	if validateOnly {
		fmt.Println("面板配置检查通过")
		return
	}
	store, err := panel.OpenStore(c.StateFile)
	if err != nil {
		log.Fatal(err)
	}
	server := panel.NewServer(c, store)
	bot := panel.NewBot(server)
	server.Notify = bot.Notify
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go bot.Run(ctx)
	log.Print("panel listening on ", c.Listen)
	if err = server.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
