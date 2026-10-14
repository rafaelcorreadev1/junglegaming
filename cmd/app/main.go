package main

import (
	"fmt"
	"os"

	"go.uber.org/fx"

	"github.com/rafaelcorreadev1/junglegaming/internal/app"
	"github.com/rafaelcorreadev1/junglegaming/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fx.New(app.Options(cfg)).Run()
}
