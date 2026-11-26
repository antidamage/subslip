// doctor.go implements `subslip doctor`. The checks live in internal/doctor.
//
// Works with: main.go (dispatch, loadConfig).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/antidamage/subslip/internal/doctor"
)

func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	env := envFlag(fs)
	user := fs.String("user", os.Getenv("SUBSLIP_DOCTOR_USER"), "Subsonic username to test with")
	password := fs.String("password", os.Getenv("SUBSLIP_DOCTOR_PASSWORD"), "Subsonic password to test with")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Parse(args)

	cfg, err := loadConfig(env())
	if err != nil {
		fmt.Fprintln(os.Stderr, "subslip:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	checks := doctor.Run(ctx, doctor.Options{Config: cfg, User: *user, Password: *password, Version: version})
	if *asJSON {
		doctor.WriteJSON(os.Stdout, checks)
	} else {
		doctor.WriteText(os.Stdout, checks)
	}
	if doctor.Failed(checks) {
		return 1
	}
	return 0
}
