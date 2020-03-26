// Copyright 2020 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package argument provides mechanisms for developers to specify extra arguments
// for the boot subcommand that are not explicitly written in boot.go. This is a
// fairly restrictive pipeline only meant to allow for the specification of top-level
// runsc args that result in additional arguments being passed to the boot subcommand;
// it may be generalized further in the future.
package argument

import (
	"os"

	"gvisor.dev/gvisor/runsc/flag"
)

// ExtraArgs is a list of arguments that can be set in runsc
var ExtraArgs = new([]Argument)

// Register adds the argument to a list of arguments that will later
// have AddToFlagset called on its elements.
func Register(arg Argument, args *[]Argument) {
	*args = append(*args, arg)
}

// Argument provides an interface for devs to add their own arguments to runsc.
type Argument interface {
	// AddToFlagset adds the command line flag to a flagset, such that
	// flagset.Parse() will parse the argument appropriately. Take note that
	// nil may be passed into this function in order to allow args to use
	//`flags.Bool()`, etc, so arguments should perform nil checks when receiving
	//a non-nil flagset.
	AddToFlagset(f *flag.FlagSet)
	// OnCreateSandboxProcess is a hook that is evaluated near the end of
	// createSandboxProcess(). Any strings returned in extraArgs will be appended
	// to cmd.Args, and any files returned in extraFiles will be appended to
	// cmd.ExtraFiles. Implementations are responsible for updating nextFD.
	OnCreateSandboxProcess(id string, nextFD *int) (extraArgs []string, extraFiles []*os.File, err error)
	// OnBoot will be evaluated when a new kernel loader is created.
	OnBoot() error
}
