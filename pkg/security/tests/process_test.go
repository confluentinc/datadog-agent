// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build functionaltests
// +build functionaltests

package tests

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/avast/retry-go"
	"github.com/davecgh/go-spew/spew"
	"github.com/oliveagle/jsonpath"
	"github.com/stretchr/testify/assert"
	"github.com/syndtr/gocapability/capability"

	sprobe "github.com/DataDog/datadog-agent/pkg/security/probe"
	"github.com/DataDog/datadog-agent/pkg/security/secl/model"
	"github.com/DataDog/datadog-agent/pkg/security/secl/rules"
)

func TestProcess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ruleDef := &rules.RuleDefinition{
		ID:         "test_rule",
		Expression: fmt.Sprintf(`process.user != "" && process.file.name == "%s" && open.file.path == "{{.Root}}/test-process"`, path.Base(executable)),
	}

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{ruleDef}, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	test.WaitSignal(t, func() error {
		testFile, _, err := test.Create("test-process")
		if err != nil {
			return err
		}
		return os.Remove(testFile)
	}, func(event *sprobe.Event, rule *rules.Rule) {
		assertTriggeredRule(t, rule, "test_rule")
	})
}

func TestProcessContext(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ruleDefs := []*rules.RuleDefinition{
		{
			ID:         "test_rule_inode",
			Expression: `open.file.path == "{{.Root}}/test-process-context" && open.flags & O_CREAT != 0`,
		},
		{
			ID:         "test_exec_time_1",
			Expression: `open.file.path == "{{.Root}}/test-exec-time-1" && process.created_at == 0`,
		},
		{
			ID:         "test_exec_time_2",
			Expression: `open.file.path == "{{.Root}}/test-exec-time-2" && process.created_at > 1s`,
		},
		{
			ID:         "test_rule_ancestors",
			Expression: `open.file.path == "{{.Root}}/test-process-ancestors" && process.ancestors[_].file.name in ["dash", "bash"]`,
		},
		{
			ID:         "test_rule_pid1",
			Expression: `open.file.path == "{{.Root}}/test-process-pid1" && process.ancestors[_].pid == 1`,
		},
		{
			ID:         "test_rule_args_envs",
			Expression: `exec.file.name == "ls" && exec.args in [~"*-al*"] && exec.envs in [~"LD_*"]`,
		},
		{
			ID:         "test_rule_argv",
			Expression: `exec.argv in ["-ll"]`,
		},
		{
			ID:         "test_rule_args_flags",
			Expression: `exec.args_flags == "l" && exec.args_flags == "s" && exec.args_flags == "escape"`,
		},
		{
			ID:         "test_rule_args_options",
			Expression: `exec.args_options in ["block-size=123"]`,
		},
		{
			ID:         "test_rule_tty",
			Expression: `open.file.path == "{{.Root}}/test-process-tty" && open.flags & O_CREAT == 0`,
		},
		{
			ID:         "test_rule_ancestors_args",
			Expression: `open.file.path == "{{.Root}}/test-ancestors-args" && process.ancestors.args_flags == "c" && process.ancestors.args_flags == "x"`,
		},
		{
			ID:         "test_rule_envp",
			Expression: `exec.file.name == "ls" && exec.envp in ["ENVP=test"] && exec.args =~ "*example.com"`,
		},
		{
			ID:         "test_rule_args_envs_dedup",
			Expression: `exec.file.name == "ls" && exec.argv == "test123456"`,
		},
		{
			ID:         "test_rule_ancestors_glob",
			Expression: `exec.file.name == "ls" && exec.argv == "glob" && process.ancestors.file.path =~ "/usr/**"`,
		},
		{
			ID:         "test_self_exec",
			Expression: `exec.file.name == "syscall_tester" && exec.argv0 == "selfexec123" && process.comm == "exe"`,
		},
		{
			ID:         "test_rule_ctx_1",
			Expression: fmt.Sprintf(`open.file.path == "{{.Root}}/test-process-ctx-1" && process.file.name == "%s"`, path.Base(executable)),
		},
		{
			ID:         "test_rule_ctx_2",
			Expression: `open.file.path == "{{.Root}}/test-process-ctx-2" && process.file.name != ""`,
		},
		{
			ID:         "test_rule_ctx_3",
			Expression: fmt.Sprintf(`open.file.path == "{{.Root}}/test-process-ctx-3" && process.file.path == "%s"`, executable),
		},
		{
			ID:         "test_rule_ctx_4",
			Expression: `open.file.path == "{{.Root}}/test-process-ctx-4" && process.file.path != ""`,
		},
		{
			ID:         "test_rule_container",
			Expression: `exec.file.name == "touch" && exec.argv == "{{.Root}}/test-container"`,
		},
	}

	test, err := newTestModule(t, nil, ruleDefs, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("exec-time", func(t *testing.T) {
		testFile, _, err := test.Path("test-exec-time-1")
		if err != nil {
			t.Fatal(err)
		}

		err = test.GetSignal(t, func() error {
			f, err := os.Create(testFile)
			if err != nil {
				return err
			}
			f.Close()
			os.Remove(testFile)

			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			t.Errorf("shouldn't get an event: got event: %s", event)
		})
		if err == nil {
			t.Fatal("shouldn't get an event")
		}

		test.WaitSignal(t, func() error {
			testFile, _, err := test.Path("test-exec-time-2")
			if err != nil {
				t.Fatal(err)
			}

			f, err := os.Create(testFile)
			if err != nil {
				return err
			}
			f.Close()
			os.Remove(testFile)

			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_exec_time_2")
		})
	})

	t.Run("inode", func(t *testing.T) {
		testFile, _, err := test.Path("test-process-context")
		if err != nil {
			t.Fatal(err)
		}

		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}

		test.WaitSignal(t, func() error {
			f, err := os.Create(testFile)
			if err != nil {
				return err
			}

			return f.Close()
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertFieldEqual(t, event, "process.file.path", executable)
			assert.Equal(t, getInode(t, executable), event.ResolveProcessCacheEntry().FileEvent.Inode, "wrong inode")
		})
	})

	test.Run(t, "args-envs", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		args := []string{"-al", "--password", "secret", "--custom", "secret"}
		envs := []string{"LD_LIBRARY_PATH=/tmp/lib", "DD_API_KEY=dd-api-key"}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc("ls", args, envs)
			// we need to ignore the error because "--password" is not a valid option for ls
			_ = cmd.Run()
			return nil
		}, validateExecEvent(t, kind, func(event *sprobe.Event, rule *rules.Rule) {
			argv0, err := event.GetFieldValue("exec.argv0")
			if err != nil {
				t.Errorf("not able to get argv0")
			}
			assert.Equal(t, "ls", argv0, "incorrect argv0: %s", argv0)

			// args
			args, err := event.GetFieldValue("exec.args")
			if err != nil || len(args.(string)) == 0 {
				t.Error("not able to get args")
			}

			contains := func(s string) bool {
				for _, arg := range strings.Split(args.(string), " ") {
					if s == arg {
						return true
					}
				}
				return false
			}

			if !contains("-al") || !contains("--password") || !contains("--custom") {
				t.Error("arg not found")
			}

			// envs
			envs, err := event.GetFieldValue("exec.envs")
			if err != nil || len(envs.([]string)) == 0 {
				t.Error("not able to get envs")
			}

			contains = func(s string) bool {
				for _, env := range envs.([]string) {
					if strings.Contains(env, s) {
						return true
					}
				}
				return false
			}

			if !contains("LD_LIBRARY_PATH") {
				t.Errorf("env not found: %v", event)
			}

			// trigger serialization to test scrubber
			str := event.String()

			if !strings.Contains(str, "password") || !strings.Contains(str, "custom") {
				t.Error("args not serialized")
			}

			if strings.Contains(str, "secret") || strings.Contains(str, "dd-api-key") {
				t.Error("secret or env values exposed")
			}
		}))
	})

	test.Run(t, "envp", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		args := []string{"-al", "http://example.com"}
		envs := []string{"ENVP=test"}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc("ls", args, envs)
			_ = cmd.Run()
			return nil
		}, validateExecEvent(t, kind, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_rule_envp", rule.ID, "wrong rule triggered")
		}))
	})

	t.Run("argv", func(t *testing.T) {
		lsExecutable := which(t, "ls")

		test.WaitSignal(t, func() error {
			cmd := exec.Command(lsExecutable, "-ll")
			return cmd.Run()
		}, validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_rule_argv")
		}))
	})

	t.Run("args-flags", func(t *testing.T) {
		lsExecutable := which(t, "ls")

		test.WaitSignal(t, func() error {
			cmd := exec.Command(lsExecutable, "-ls", "--escape")
			return cmd.Run()
		}, validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_rule_args_flags")
		}))
	})

	t.Run("args-options", func(t *testing.T) {
		lsExecutable := which(t, "ls")

		test.WaitSignal(t, func() error {
			cmd := exec.Command(lsExecutable, "--block-size", "123")
			return cmd.Run()
		}, validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_rule_args_options")
		}))
	})

	test.Run(t, "args-overflow", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		args := []string{"-al"}
		envs := []string{"LD_LIBRARY_PATH=/tmp/lib"}

		// size overflow
		var long string
		for i := 0; i != 1024; i++ {
			long += "a"
		}
		args = append(args, long)

		test.WaitSignal(t, func() error {
			cmd := cmdFunc("ls", args, envs)
			// we need to ignore the error because the string of "a" generates a "File name too long" error
			_ = cmd.Run()
			return nil
		}, validateExecEvent(t, kind, func(event *sprobe.Event, rule *rules.Rule) {
			args, err := event.GetFieldValue("exec.args")
			if err != nil {
				t.Errorf("not able to get args")
			}

			argv := strings.Split(args.(string), " ")
			assert.Equal(t, 2, len(argv), "incorrect number of args: %s", argv)
			assert.Equal(t, 254, len(argv[1]), "wrong arg length")
			assert.Equal(t, true, strings.HasSuffix(argv[1], "..."), "args not truncated")

			argv0, err := event.GetFieldValue("exec.argv0")
			if err != nil {
				t.Errorf("not able to get argv0")
			}
			assert.Equal(t, "ls", argv0, "incorrect argv0: %s", argv0)
		}))

		var arg string
		for i := 0; i != 300; i++ {
			arg += "a"
		}

		// number of args overflow
		nArgs, args := 200, []string{"-al"}
		for i := 0; i != nArgs; i++ {
			args = append(args, arg)
		}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc("ls", args, envs)
			// we need to ignore the error because the string of "a" generates a "File name too long" error
			_ = cmd.Run()
			return nil
		}, validateExecEvent(t, kind, func(event *sprobe.Event, rule *rules.Rule) {
			args, err := event.GetFieldValue("exec.args")
			if err != nil {
				t.Errorf("not able to get args")
			}

			argv := strings.Split(args.(string), " ")
			assert.Equal(t, 159, len(argv), "incorrect number of args: %s", argv)

			truncated, err := event.GetFieldValue("exec.args_truncated")
			if err != nil {
				t.Errorf("not able to get args truncated")
			}
			if !truncated.(bool) {
				t.Errorf("arg not truncated: %s", args.(string))
			}
		}))
	})

	t.Run("tty", func(t *testing.T) {
		testFile, _, err := test.Path("test-process-tty")
		if err != nil {
			t.Fatal(err)
		}

		f, err := os.Create(testFile)
		if err != nil {
			t.Fatal(err)
		}

		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(testFile)

		executable := which(t, "tail")

		test.WaitSignal(t, func() error {
			var wg sync.WaitGroup

			errChan := make(chan error, 1)

			wg.Add(1)
			go func() {
				defer wg.Done()

				time.Sleep(2 * time.Second)
				cmd := exec.Command("script", "/dev/null", "-c", executable+" -f "+testFile)
				if err := cmd.Start(); err != nil {
					errChan <- err
					return
				}
				time.Sleep(2 * time.Second)

				cmd.Process.Kill()
				cmd.Wait()
			}()

			wg.Wait()

			select {
			case err = <-errChan:
				return err
			default:
			}
			return nil

		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertFieldEqual(t, event, "process.file.path", executable)

			if name, _ := event.GetFieldValue("process.tty_name"); !strings.HasPrefix(name.(string), "pts") {
				t.Errorf("not able to get a tty name: %s\n", name)
			}

			if inode := getInode(t, executable); inode != event.ResolveProcessCacheEntry().FileEvent.Inode {
				t.Errorf("expected inode %d, got %d => %+v", event.ResolveProcessCacheEntry().FileEvent.Inode, inode, event)
			}

			str := event.String()

			if !strings.Contains(str, "pts") {
				t.Error("tty not serialized")
			}
		})
	})

	test.Run(t, "ancestors", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		testFile, _, err := test.Path("test-process-ancestors")
		if err != nil {
			t.Fatal(err)
		}

		executable := "touch"

		// Bash attempts to optimize away forks in the last command in a function body
		// under appropriate circumstances (source: bash changelog)
		args := []string{"-c", "$(" + executable + " " + testFile + ")"}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc("sh", args, nil)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_rule_ancestors")
			assert.Equal(t, "sh", event.ProcessContext.Ancestor.Comm)
		})
	})

	test.Run(t, "pid1", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		testFile, _, err := test.Path("test-process-pid1")
		if err != nil {
			t.Fatal(err)
		}

		shell, executable := "sh", "touch"

		// Bash attempts to optimize away forks in the last command in a function body
		// under appropriate circumstances (source: bash changelog)
		args := []string{"-c", "$(" + executable + " " + testFile + ")"}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc(shell, args, nil)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_rule_pid1", rule.ID, "wrong rule triggered")
		})
	})

	test.Run(t, "service-tag", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		testFile, _, err := test.Path("test-process-context")
		if err != nil {
			t.Fatal(err)
		}

		shell, executable := "sh", "touch"

		// Bash attempts to optimize away forks in the last command in a function body
		// under appropriate circumstances (source: bash changelog)
		args := []string{"-c", "$(" + executable + " " + testFile + ")"}
		envs := []string{"DD_SERVICE=myservice"}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc(shell, args, envs)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_rule_inode", rule.ID, "wrong rule triggered")

			service := event.GetProcessServiceTag()
			assert.Equal(t, service, "myservice")
		})
	})

	test.Run(t, "ancestors-args", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		testFile, _, err := test.Path("test-ancestors-args")
		if err != nil {
			t.Fatal(err)
		}

		shell, executable := "sh", "touch"
		args := []string{"-x", "-c", "$(" + executable + " " + testFile + ")"}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc(shell, args, nil)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_rule_ancestors_args", rule.ID, "wrong rule triggered")
		})
	})

	test.Run(t, "args-envs-dedup", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		shell, args, envs := "sh", []string{"-x", "-c", "ls -al test123456; echo"}, []string{"DEDUP=dedup123"}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc(shell, args, envs)
			_ = cmd.Run()
			return nil
		}, validateExecEvent(t, kind, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_rule_args_envs_dedup", rule.ID, "wrong rule triggered")

			var data interface{}
			serialized := event.String()
			if err := json.Unmarshal([]byte(serialized), &data); err != nil {
				t.Error(err)
			}

			if json, err := jsonpath.JsonPathLookup(data, "$.process.ancestors[0].args"); err == nil {
				t.Errorf("shouldn't have args, got %+v (%s)", json, spew.Sdump(data))
			}

			if json, err := jsonpath.JsonPathLookup(data, "$.process.ancestors[0].envs"); err == nil {
				t.Errorf("shouldn't have envs, got %+v (%s)", json, spew.Sdump(data))
			}

			if json, err := jsonpath.JsonPathLookup(data, "$.process.ancestors[1].args"); err != nil {
				t.Errorf("should have args, got %+v (%s)", json, spew.Sdump(data))
			}

			if json, err := jsonpath.JsonPathLookup(data, "$.process.ancestors[1].envs"); err != nil {
				t.Errorf("should have envs, got %+v (%s)", json, spew.Sdump(data))
			}
		}))
	})

	t.Run("ancestors-glob", func(t *testing.T) {
		lsExecutable := which(t, "ls")

		test.WaitSignal(t, func() error {
			cmd := exec.Command(lsExecutable, "glob")
			_ = cmd.Run()
			return nil
		}, validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_rule_ancestors_glob")
		}))
	})

	test.Run(t, "self-exec", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		args := []string{"self-exec", "selfexec123", "abc"}
		envs := []string{}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc(syscallTester, args, envs)
			_, _ = cmd.CombinedOutput()

			return nil
		}, validateExecEvent(t, kind, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_self_exec")
		}))
	})

	test.Run(t, "container-id", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		testFile, _, err := test.Path("test-container")
		if err != nil {
			t.Fatal(err)
		}

		// Bash attempts to optimize away forks in the last command in a function body
		// under appropriate circumstances (source: bash changelog)
		args := []string{"-c", "$(touch " + testFile + ")"}

		test.WaitSignal(t, func() error {
			cmd := cmdFunc("sh", args, nil)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			return nil
		}, validateExecEvent(t, kind, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_rule_container", rule.ID, "wrong rule triggered")

			if kind == dockerWrapperType {
				assert.Equal(t, event.Exec.Process.ContainerID, event.ProcessCacheEntry.ContainerID)
				assert.Equal(t, event.Exec.Process.ContainerID, event.ProcessCacheEntry.Ancestor.ContainerID)
			}
		}))
	})

	testProcessContextRule := func(t *testing.T, ruleID, filename string) {
		test.Run(t, ruleID, func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
			testFile, _, err := test.Path(filename)
			if err != nil {
				t.Fatal(err)
			}

			test.WaitSignal(t, func() error {
				f, err := os.Create(testFile)
				if err != nil {
					return err
				}
				f.Close()
				return os.Remove(testFile)
			}, func(event *sprobe.Event, rule *rules.Rule) {
				assert.Equal(t, ruleID, rule.ID, "wrong rule triggered")
			})
		})
	}

	testProcessContextRule(t, "test_rule_ctx_1", "test-process-ctx-1")
	testProcessContextRule(t, "test_rule_ctx_2", "test-process-ctx-2")
	testProcessContextRule(t, "test_rule_ctx_3", "test-process-ctx-3")
	testProcessContextRule(t, "test_rule_ctx_4", "test-process-ctx-4")
}

func TestProcessEnvsWithValue(t *testing.T) {
	lsExec := which(t, "ls")
	ruleDefs := []*rules.RuleDefinition{
		{
			ID:         "test_ldpreload_from_tmp_with_envs",
			Expression: fmt.Sprintf(`exec.file.path == "%s" && exec.envs in [~"LD_PRELOAD=*/tmp/*"]`, lsExec),
		},
	}

	opts := testOpts{
		envsWithValue: []string{"LD_PRELOAD"},
	}

	test, err := newTestModule(t, nil, ruleDefs, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	t.Run("ldpreload", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			args := []string{}
			envp := []string{"LD_PRELOAD=/tmp/dyn.so"}

			cmd := exec.Command(lsExec, args...)
			cmd.Env = envp
			_ = cmd.Run()
			return nil
		}, validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_ldpreload_from_tmp_with_envs")
			assertFieldEqual(t, event, "exec.file.path", lsExec)
			assertFieldStringArrayIndexedOneOf(t, event, "exec.envs", 0, []string{"LD_PRELOAD=/tmp/dyn.so"})
		}))
	})
}

func TestProcessExecCTime(t *testing.T) {
	executable := which(t, "touch")

	ruleDef := &rules.RuleDefinition{
		ID:         "test_exec_ctime",
		Expression: "exec.file.change_time < 5s",
	}

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{ruleDef}, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	test.WaitSignal(t, func() error {
		testFile, _, err := test.Path("touch")
		if err != nil {
			return err
		}
		copyFile(executable, testFile, 0755)

		cmd := exec.Command(testFile, "/tmp/test")
		return cmd.Run()
	}, validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
		assert.Equal(t, "test_exec_ctime", rule.ID, "wrong rule triggered")
	}))
}

func TestProcessPIDVariable(t *testing.T) {
	executable := which(t, "touch")

	ruleDef := &rules.RuleDefinition{
		ID:         "test_rule_var",
		Expression: `open.file.path =~ "/proc/*/maps" && open.file.path != "/proc/${process.pid}/maps"`,
	}

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{ruleDef}, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	test.WaitSignal(t, func() error {
		cmd := exec.Command(executable, fmt.Sprintf("/proc/%d/maps", os.Getpid()))
		return cmd.Run()
	}, func(event *sprobe.Event, rule *rules.Rule) {
		assert.Equal(t, "test_rule_var", rule.ID, "wrong rule triggered")
	})
	if err != nil {
		t.Error(err)
	}
}

func TestProcessMutableVariable(t *testing.T) {
	ruleDefs := []*rules.RuleDefinition{{
		ID:         "test_rule_set_mutable_vars",
		Expression: `open.file.path == "{{.Root}}/test-open"`,
		Actions: []rules.ActionDefinition{{
			Set: &rules.SetDefinition{
				Name:  "var1",
				Value: true,
				Scope: "process",
			},
		}, {
			Set: &rules.SetDefinition{
				Name:  "var2",
				Value: "disabled",
				Scope: "process",
			},
		}, {
			Set: &rules.SetDefinition{
				Name:  "var3",
				Value: []string{"aaa"},
				Scope: "process",
			},
		}, {
			Set: &rules.SetDefinition{
				Name:  "var3",
				Value: []string{"aaa"},
				Scope: "process",
			},
		}, {
			Set: &rules.SetDefinition{
				Name:  "var4",
				Field: "process.file.name",
			},
		}, {
			Set: &rules.SetDefinition{
				Name:  "var5",
				Field: "open.file.path",
			},
		}},
	}, {
		ID:         "test_rule_modify_mutable_vars",
		Expression: `open.file.path == "{{.Root}}/test-open-2"`,
		Actions: []rules.ActionDefinition{{
			Set: &rules.SetDefinition{
				Name:  "var2",
				Value: "enabled",
				Scope: "process",
			},
		}, {
			Set: &rules.SetDefinition{
				Name:   "var3",
				Value:  []string{"bbb"},
				Scope:  "process",
				Append: true,
			},
		}},
	}, {
		ID: "test_rule_test_mutable_vars",
		Expression: `open.file.path == "{{.Root}}/test-open-3"` +
			`&& ${process.var1} == true` +
			`&& ${process.var2} == "enabled"` +
			`&& "aaa" in ${process.var3}` +
			`&& "bbb" in ${process.var3}` +
			`&& process.file.name == "${var4}"` +
			`&& open.file.path == "${var5}-3"`,
	}}

	test, err := newTestModule(t, nil, ruleDefs, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	var filename1, filename2, filename3 string

	test.WaitSignal(t, func() error {
		filename1, _, err = test.Create("test-open")
		return err
	}, func(event *sprobe.Event, rule *rules.Rule) {
		assert.Equal(t, "test_rule_set_mutable_vars", rule.ID, "wrong rule triggered")
	})
	if err != nil {
		t.Error(err)
	}
	defer os.Remove(filename1)

	test.WaitSignal(t, func() error {
		filename2, _, err = test.Create("test-open-2")
		return err
	}, func(event *sprobe.Event, rule *rules.Rule) {
		assert.Equal(t, "test_rule_modify_mutable_vars", rule.ID, "wrong rule triggered")
	})
	if err != nil {
		t.Error(err)
	}
	defer os.Remove(filename2)

	test.WaitSignal(t, func() error {
		filename3, _, err = test.Create("test-open-3")
		return err
	}, func(event *sprobe.Event, rule *rules.Rule) {
		assert.Equal(t, "test_rule_test_mutable_vars", rule.ID, "wrong rule triggered")
	})
	if err != nil {
		t.Error(err)
	}
	defer os.Remove(filename3)
}

func TestProcessExec(t *testing.T) {
	executable := which(t, "touch")

	ruleDef := &rules.RuleDefinition{
		ID:         "test_rule",
		Expression: fmt.Sprintf(`exec.file.path == "%s"`, executable),
	}

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{ruleDef}, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	test.WaitSignal(t, func() error {
		cmd := exec.Command("sh", "-c", executable+" /dev/null")
		return cmd.Run()
	}, validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
		assertFieldEqual(t, event, "exec.file.path", executable)
		// TODO: use `process.ancestors[0].file.name` directly when this feature is reintroduced
		assertFieldStringArrayIndexedOneOf(t, event, "process.ancestors.file.name", 0, []string{"sh", "bash", "dash"})
	}))
}

func TestProcessMetadata(t *testing.T) {
	ruleDefs := []*rules.RuleDefinition{
		{
			ID:         "test_executable",
			Expression: `exec.file.path == "{{.Root}}/test-exec" && exec.file.uid == 98 && exec.file.gid == 99`,
		},
		{
			ID:         "test_metadata",
			Expression: `exec.file.path == "{{.Root}}/test-exec" && process.uid == 1001 && process.euid == 1002 && process.fsuid == 1002 && process.gid == 2001 && process.egid == 2002 && process.fsgid == 2002`,
		},
	}

	test, err := newTestModule(t, nil, ruleDefs, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	fileMode := uint16(0o777)
	testFile, _, err := test.CreateWithOptions("test-exec", 98, 99, int(fileMode))
	if err != nil {
		t.Fatal(err)
	}

	if err = os.Chmod(testFile, os.FileMode(fileMode)); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(testFile, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("#!/bin/bash\n")
	f.Close()

	t.Run("executable", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			cmd := exec.Command(testFile)
			return cmd.Run()
		}, validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "exec", event.GetType(), "wrong event type")
			assertRights(t, event.Exec.FileEvent.Mode, fileMode)
			assertNearTime(t, event.Exec.FileEvent.MTime)
			assertNearTime(t, event.Exec.FileEvent.CTime)
		}))
	})

	t.Run("credentials", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			attr := &syscall.ProcAttr{
				Sys: &syscall.SysProcAttr{
					Credential: &syscall.Credential{
						Uid: 1001,
						Gid: 2001,
					},
				},
			}
			_, err := syscall.ForkExec(testFile, []string{}, attr)
			return err
		}, validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "exec", event.GetType(), "wrong event type")
			assert.Equal(t, 1001, int(event.Exec.Credentials.UID), "wrong uid")
			assert.Equal(t, 1001, int(event.Exec.Credentials.EUID), "wrong euid")
			assert.Equal(t, 1001, int(event.Exec.Credentials.FSUID), "wrong fsuid")
			assert.Equal(t, 2001, int(event.Exec.Credentials.GID), "wrong gid")
			assert.Equal(t, 2001, int(event.Exec.Credentials.EGID), "wrong egid")
			assert.Equal(t, 2001, int(event.Exec.Credentials.FSGID), "wrong fsgid")
		}))
	})
}

func TestProcessExecExit(t *testing.T) {
	executable := which(t, "touch")

	rule := &rules.RuleDefinition{
		ID:         "test_rule",
		Expression: fmt.Sprintf(`exec.file.path == "%s" && exec.args in [~"*01010101*"]`, executable),
	}

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{rule}, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	var execPid uint32

	err = test.GetProbeEvent(func() error {
		cmd := exec.Command(executable, "-t", "01010101", "/dev/null")
		return cmd.Run()
	}, func(event *sprobe.Event) bool {
		switch event.GetEventType() {
		case model.ExecEventType:
			if basename, err := event.GetFieldValue("exec.file.name"); err != nil || basename.(string) != "touch" {
				return false
			}

			if args, err := event.GetFieldValue("exec.args"); err != nil || !strings.Contains(args.(string), "01010101") {
				return false
			}

			validate := validateExecEvent(t, noWrapperType, func(event *sprobe.Event, rule *rules.Rule) {
				if !validateProcessContextLineage(t, event) {
					t.Error(event.String())
				}

				if !validateProcessContextSECL(t, event) {
					t.Error(event.String())
				}

				assertFieldEqual(t, event, "exec.file.name", "touch")
				assertFieldContains(t, event, "exec.args", "01010101")
			})
			validate(event, nil)

			execPid = event.ProcessContext.Pid

		case model.ExitEventType:
			// assert that exit time >= exec time
			assert.False(t, event.ProcessCacheEntry.ExitTime.Before(event.ProcessCacheEntry.ExecTime), "exit time < exec time")
			if execPid != 0 && event.ProcessContext.Pid == execPid {
				return true
			}
		}
		return false
	}, 3*time.Second, model.ExecEventType, model.ExitEventType)
	if err != nil {
		t.Error(err)
	}

	assert.NotEmpty(t, execPid, "exec pid not found")

	// make sure that the process cache entry of the process was properly deleted from the cache
	err = retry.Do(func() error {
		resolvers := test.probe.GetResolvers()
		entry := resolvers.ProcessResolver.Get(execPid)
		if entry != nil {
			return errors.New("the process cache entry was not deleted from the user space cache")
		}

		return nil
	})

	if err != nil {
		t.Error(err)
	}
}

func TestProcessCredentialsUpdate(t *testing.T) {
	ruleDefs := []*rules.RuleDefinition{
		{
			ID:         "test_setuid",
			Expression: `setuid.uid == 1001 && process.file.name == "syscall_tester"`,
		},
		{
			ID:         "test_setreuid",
			Expression: `setuid.uid == 1002 && setuid.euid == 1003 && process.file.name == "syscall_tester"`,
		},
		{
			ID:         "test_setfsuid",
			Expression: `setuid.fsuid == 1004 && process.file.name == "syscall_tester"`,
		},
		{
			ID:         "test_setgid",
			Expression: `setgid.gid == 1005 && process.file.name == "syscall_tester"`,
		},
		{
			ID:         "test_setregid",
			Expression: `setgid.gid == 1006 && setgid.egid == 1007 && process.file.name == "syscall_tester"`,
		},
		{
			ID:         "test_setfsgid",
			Expression: `setgid.fsgid == 1008 && process.file.name == "syscall_tester"`,
		},
		{
			ID:         "test_capset",
			Expression: `capset.cap_effective & CAP_WAKE_ALARM == 0 && capset.cap_permitted & CAP_SYS_BOOT == 0 && process.file.name == "syscall_go_tester"`,
		},
	}

	test, err := newTestModule(t, nil, ruleDefs, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	goSyscallTester, err := loadSyscallTester(t, test, "syscall_go_tester")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("setuid", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			return runSyscallTesterFunc(t, syscallTester, "process-credentials", "setuid", "1001", "0")
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_setuid")
			assert.Equal(t, uint32(1001), event.SetUID.UID, "wrong uid")
		})
	})

	t.Run("setreuid", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			return runSyscallTesterFunc(t, syscallTester, "process-credentials", "setreuid", "1002", "1003")
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_setreuid")
			assert.Equal(t, uint32(1002), event.SetUID.UID, "wrong uid")
			assert.Equal(t, uint32(1003), event.SetUID.EUID, "wrong euid")
		})
	})

	t.Run("setresuid", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			return runSyscallTesterFunc(t, syscallTester, "process-credentials", "setresuid", "1002", "1003")
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_setreuid")
			assert.Equal(t, uint32(1002), event.SetUID.UID, "wrong uid")
			assert.Equal(t, uint32(1003), event.SetUID.EUID, "wrong euid")
		})
	})

	t.Run("setfsuid", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			return runSyscallTesterFunc(t, syscallTester, "process-credentials", "setfsuid", "1004", "0")
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_setfsuid")
			assert.Equal(t, uint32(1004), event.SetUID.FSUID, "wrong fsuid")
		})
	})

	t.Run("setgid", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			return runSyscallTesterFunc(t, syscallTester, "process-credentials", "setgid", "1005", "0")
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_setgid")
			assert.Equal(t, uint32(1005), event.SetGID.GID, "wrong gid")
		})
	})

	t.Run("setregid", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			return runSyscallTesterFunc(t, syscallTester, "process-credentials", "setregid", "1006", "1007")
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_setregid")
			assert.Equal(t, uint32(1006), event.SetGID.GID, "wrong gid")
			assert.Equal(t, uint32(1007), event.SetGID.EGID, "wrong egid")
		})
	})

	t.Run("setresgid", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			return runSyscallTesterFunc(t, syscallTester, "process-credentials", "setresgid", "1006", "1007")
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_setregid")
			assert.Equal(t, uint32(1006), event.SetGID.GID, "wrong gid")
			assert.Equal(t, uint32(1007), event.SetGID.EGID, "wrong egid")
		})
	})

	t.Run("setfsgid", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			return runSyscallTesterFunc(t, syscallTester, "process-credentials", "setfsgid", "1008", "0")
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_setfsgid")
			assert.Equal(t, uint32(1008), event.SetGID.FSGID, "wrong gid")
		})
	})

	t.Run("capset", func(t *testing.T) {
		// Parse kernel capabilities of the current thread
		threadCapabilities, err := capability.NewPid2(0)
		if err != nil {
			t.Fatal(err)
		}
		if err := threadCapabilities.Load(); err != nil {
			t.Fatal(err)
		}
		// remove capabilities that are removed by syscall_go_tester
		threadCapabilities.Unset(capability.PERMITTED|capability.EFFECTIVE, capability.CAP_SYS_BOOT)
		threadCapabilities.Unset(capability.EFFECTIVE, capability.CAP_WAKE_ALARM)

		test.WaitSignal(t, func() error {
			return runSyscallTesterFunc(t, goSyscallTester, "-process-credentials-capset")
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_capset")

			// transform the collected kernel capabilities into a usable capability set
			newSet, err := capability.NewPid2(0)
			if err != nil {
				t.Error(err)
			}
			newSet.Clear(capability.PERMITTED | capability.EFFECTIVE)
			parseCapIntoSet(event.Capset.CapEffective, capability.EFFECTIVE, newSet, t)
			parseCapIntoSet(event.Capset.CapPermitted, capability.PERMITTED, newSet, t)

			for _, c := range capability.List() {
				if expectedValue := threadCapabilities.Get(capability.EFFECTIVE, c); expectedValue != newSet.Get(capability.EFFECTIVE, c) {
					t.Errorf("expected incorrect %s flag in cap_effective, expected %v", c, expectedValue)
				}
				if expectedValue := threadCapabilities.Get(capability.PERMITTED, c); expectedValue != newSet.Get(capability.PERMITTED, c) {
					t.Errorf("expected incorrect %s flag in cap_permitted, expected %v", c, expectedValue)
				}
			}
		})
	})
}

func parseCapIntoSet(capabilities uint64, flag capability.CapType, c capability.Capabilities, t *testing.T) {
	for _, v := range model.KernelCapabilityConstants {
		if v == 0 {
			continue
		}

		if capabilities&v == v {
			c.Set(flag, capability.Cap(math.Log2(float64(v))))
		}
	}
}

func TestProcessIsThread(t *testing.T) {
	const openTriggerFilename = "test-isthread"
	ruleDefs := []*rules.RuleDefinition{
		{
			ID:         "test_process_fork_is_thread",
			Expression: fmt.Sprintf(`open.file.name == "%s" && process.file.name == "syscall_tester" && process.ancestors.file.name == "syscall_tester" && process.is_thread`, openTriggerFilename),
		},
		{
			ID:         "test_process_exec_is_not_thread",
			Expression: fmt.Sprintf(`open.file.name == "%s" && process.file.name == "syscall_tester" && process.ancestors.file.name == "syscall_tester" && !process.is_thread`, openTriggerFilename),
		},
	}

	test, err := newTestModule(t, nil, ruleDefs, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("fork-isthread", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			args := []string{"fork", openTriggerFilename}
			cmd := exec.Command(syscallTester, args...)
			_ = cmd.Run()
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_process_fork_is_thread")
			assert.Equal(t, openTriggerFilename, event.Open.File.BasenameStr, "wrong opened file basename")
			assert.Equal(t, "syscall_tester", event.ProcessContext.FileEvent.BasenameStr, "wrong process file basename")
			assert.Equal(t, "syscall_tester", event.ProcessContext.Ancestor.ProcessContext.FileEvent.BasenameStr, "wrong parent process file basename")
			assert.True(t, event.ProcessContext.IsThread, "process should be marked as being a thread")
		})
	})

	t.Run("exec-isnotthread", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			args := []string{"fork", "exec", openTriggerFilename}
			cmd := exec.Command(syscallTester, args...)
			_ = cmd.Run()
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assertTriggeredRule(t, rule, "test_process_exec_is_not_thread")
			assert.Equal(t, openTriggerFilename, event.Open.File.BasenameStr, "wrong opened file basename")
			assert.Equal(t, "syscall_tester", event.ProcessContext.FileEvent.BasenameStr, "wrong process file basename")
			assert.Equal(t, "syscall_tester", event.ProcessContext.Ancestor.ProcessContext.FileEvent.BasenameStr, "wrong parent process file basename")
			assert.False(t, event.ProcessContext.IsThread, "process should be marked as not being a thread")
		})
	})
}

func TestProcessExit(t *testing.T) {
	sleepExec := which(t, "sleep")
	timeoutExec := which(t, "timeout")

	const envpExitSleep = "TESTPROCESSEXITSLEEP=1"
	const envpExitSleepTime = "TESTPROCESSEXITSLEEPTIME=1"

	ruleDefs := []*rules.RuleDefinition{
		{
			ID:         "test_exit_ok",
			Expression: fmt.Sprintf(`exit.cause == EXITED && exit.code == 0 && process.file.path == "%s" && process.envp in ["%s"]`, sleepExec, envpExitSleep),
		},
		{
			ID:         "test_exit_error",
			Expression: fmt.Sprintf(`exit.cause == EXITED && exit.code == 1 && process.file.path == "%s" && process.envp in ["%s"]`, sleepExec, envpExitSleep),
		},
		{
			ID:         "test_exit_coredump",
			Expression: fmt.Sprintf(`exit.cause == COREDUMPED && exit.code == SIGQUIT && process.file.path == "%s" && process.envp in ["%s"]`, sleepExec, envpExitSleep),
		},
		{
			ID:         "test_exit_signal",
			Expression: fmt.Sprintf(`exit.cause == SIGNALED && exit.code == SIGKILL && process.file.path == "%s" && process.envp in ["%s"]`, sleepExec, envpExitSleep),
		},
		{
			ID:         "test_exit_time_1",
			Expression: fmt.Sprintf(`exit.cause == EXITED && exit.code == 0 && process.created_at < 2s && process.file.path == "%s" && process.envp in ["%s"]`, sleepExec, envpExitSleepTime),
		},
		{
			ID:         "test_exit_time_2",
			Expression: fmt.Sprintf(`exit.cause == EXITED && exit.code == 0 && process.created_at > 2s && process.file.path == "%s" && process.envp in ["%s"]`, sleepExec, envpExitSleepTime),
		},
	}

	test, err := newTestModule(t, nil, ruleDefs, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	t.Run("exit-ok", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			args := []string{"0"}
			envp := []string{envpExitSleep}

			cmd := exec.Command(sleepExec, args...)
			cmd.Env = envp
			return cmd.Run()
		}, func(event *sprobe.Event, rule *rules.Rule) {
			if !validateExitSchema(t, event) {
				t.Error(event.String())
			}
			assertTriggeredRule(t, rule, "test_exit_ok")
			assertFieldEqual(t, event, "exit.file.path", sleepExec)
			assert.Equal(t, uint32(model.ExitExited), event.Exit.Cause, "wrong exit cause")
			assert.Equal(t, uint32(0), event.Exit.Code, "wrong exit code")
			assert.False(t, event.ProcessCacheEntry.ExitTime.Before(event.ProcessCacheEntry.ExecTime), "exit time < exec time")
		})
	})

	t.Run("exit-error", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			args := []string{} // sleep with no argument should exit with return code 1
			envp := []string{envpExitSleep}

			cmd := exec.Command(sleepExec, args...)
			cmd.Env = envp
			_ = cmd.Run()
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			if !validateExitSchema(t, event) {
				t.Error(event.String())
			}
			assertTriggeredRule(t, rule, "test_exit_error")
			assertFieldEqual(t, event, "exit.file.path", sleepExec)
			assert.Equal(t, uint32(model.ExitExited), event.Exit.Cause, "wrong exit cause")
			assert.Equal(t, uint32(1), event.Exit.Code, "wrong exit code")
			assert.False(t, event.ProcessCacheEntry.ExitTime.Before(event.ProcessCacheEntry.ExecTime), "exit time < exec time")
		})
	})

	t.Run("exit-coredumped", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			args := []string{"--preserve-status", "--signal=SIGQUIT", "0.1", sleepExec, "1"}
			envp := []string{envpExitSleep}

			cmd := exec.Command(timeoutExec, args...)
			cmd.Env = envp
			_ = cmd.Run()
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			if !validateExitSchema(t, event) {
				t.Error(event.String())
			}
			assertTriggeredRule(t, rule, "test_exit_coredump")
			assertFieldEqual(t, event, "exit.file.path", sleepExec)
			assert.Equal(t, uint32(model.ExitCoreDumped), event.Exit.Cause, "wrong exit cause")
			assert.Equal(t, uint32(syscall.SIGQUIT), event.Exit.Code, "wrong exit code")
			assert.False(t, event.ProcessCacheEntry.ExitTime.Before(event.ProcessCacheEntry.ExecTime), "exit time < exec time")
		})
	})

	t.Run("exit-signaled", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			args := []string{"--preserve-status", "--signal=SIGKILL", "0.1", sleepExec, "1"}
			envp := []string{envpExitSleep}

			cmd := exec.Command(timeoutExec, args...)
			cmd.Env = envp
			_ = cmd.Run()
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			if !validateExitSchema(t, event) {
				t.Error(event.String())
			}
			assertTriggeredRule(t, rule, "test_exit_signal")
			assertFieldEqual(t, event, "exit.file.path", sleepExec)
			assert.Equal(t, uint32(model.ExitSignaled), event.Exit.Cause, "wrong exit cause")
			assert.Equal(t, uint32(syscall.SIGKILL), event.Exit.Code, "wrong exit code")
			assert.False(t, event.ProcessCacheEntry.ExitTime.Before(event.ProcessCacheEntry.ExecTime), "exit time < exec time")
		})
	})

	t.Run("exit-time-1", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			args := []string{"--preserve-status", "--signal=SIGKILL", "1", sleepExec, "0.1"}
			envp := []string{envpExitSleepTime}

			cmd := exec.Command(timeoutExec, args...)
			cmd.Env = envp
			return cmd.Run()
		}, func(event *sprobe.Event, rule *rules.Rule) {
			if !validateExitSchema(t, event) {
				t.Error(event.String())
			}
			assertTriggeredRule(t, rule, "test_exit_time_1")
			assertFieldEqual(t, event, "exit.file.path", sleepExec)
			assert.Equal(t, uint32(model.ExitExited), event.Exit.Cause, "wrong exit cause")
			assert.Equal(t, uint32(0), event.Exit.Code, "wrong exit code")
			assert.False(t, event.ProcessCacheEntry.ExitTime.Before(event.ProcessCacheEntry.ExecTime), "exit time < exec time")
		})
	})

	t.Run("exit-time-2", func(t *testing.T) {
		test.WaitSignal(t, func() error {
			args := []string{"--preserve-status", "--signal=SIGKILL", "3", sleepExec, "2.1"}
			envp := []string{envpExitSleepTime}

			cmd := exec.Command(timeoutExec, args...)
			cmd.Env = envp
			return cmd.Run()
		}, func(event *sprobe.Event, rule *rules.Rule) {
			if !validateExitSchema(t, event) {
				t.Error(event.String())
			}
			assertTriggeredRule(t, rule, "test_exit_time_2")
			assertFieldEqual(t, event, "exit.file.path", sleepExec)
			assert.Equal(t, uint32(model.ExitExited), event.Exit.Cause, "wrong exit cause")
			assert.Equal(t, uint32(0), event.Exit.Code, "wrong exit code")
			assert.False(t, event.ProcessCacheEntry.ExitTime.Before(event.ProcessCacheEntry.ExecTime), "exit time < exec time")
		})
	})
}

func TestProcessBusybox(t *testing.T) {
	ruleDefs := []*rules.RuleDefinition{
		{
			ID:         "test_busybox_1",
			Expression: `exec.file.path == "/usr/bin/whoami"`,
		},
		{
			ID:         "test_busybox_2",
			Expression: `exec.file.path == "/bin/sync"`,
		},
		{
			ID:         "test_busybox_3",
			Expression: `exec.file.name == "df"`,
		},
		{
			ID:         "test_busybox_4",
			Expression: `open.file.path == "/tmp/busybox-test" && process.file.name == "touch"`,
		},
	}

	test, err := newTestModule(t, nil, ruleDefs, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	wrapper, err := newDockerCmdWrapper(test.Root())
	if err != nil {
		t.Skip("docker no available")
		return
	}
	wrapper.SetImage("alpine")

	wrapper.Run(t, "busybox-1", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		test.WaitSignal(t, func() error {
			cmd := cmdFunc("/usr/bin/whoami", nil, nil)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_busybox_1", rule.ID, "wrong rule triggered")
		})
	})

	wrapper.Run(t, "busybox-2", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		test.WaitSignal(t, func() error {
			cmd := cmdFunc("/bin/sync", nil, nil)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_busybox_2", rule.ID, "wrong rule triggered")
		})
	})

	wrapper.Run(t, "busybox-3", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		test.WaitSignal(t, func() error {
			cmd := cmdFunc("/bin/df", nil, nil)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_busybox_3", rule.ID, "wrong rule triggered")
		})
	})

	wrapper.Run(t, "busybox-4", func(t *testing.T, kind wrapperType, cmdFunc func(cmd string, args []string, envs []string) *exec.Cmd) {
		test.WaitSignal(t, func() error {
			cmd := cmdFunc("/bin/touch", []string{"/tmp/busybox-test"}, nil)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			return nil
		}, func(event *sprobe.Event, rule *rules.Rule) {
			assert.Equal(t, "test_busybox_4", rule.ID, "wrong rule triggered")
		})
	})
}

func TestProcessResolution(t *testing.T) {
	ruleDefs := []*rules.RuleDefinition{
		{
			ID:         "test_resolution",
			Expression: `open.file.path == "/tmp/test-process-resolution"`,
		},
	}

	test, err := newTestModule(t, nil, ruleDefs, testOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	var cmd *exec.Cmd
	var stdin io.WriteCloser
	defer func() {
		if cmd != nil {
			if stdin != nil {
				stdin.Close()
			}

			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		}
	}()

	test.WaitSignal(t, func() error {
		var err error

		args := []string{"multi-open", "/tmp/test-process-resolution", "/tmp/test-process-resolution"}

		cmd := exec.Command(syscallTester, args...)
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return err
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		_, err = io.WriteString(stdin, "\n")

		return err
	}, func(event *sprobe.Event, rule *rules.Rule) {
		assert.Equal(t, "test_resolution", rule.ID, "wrong rule triggered")

		value, err := event.GetFieldValue("process.pid")
		if err != nil {
			t.Errorf("not able to get pid")
		}
		pid := uint32(value.(int))

		resolvers := test.probe.GetResolvers()

		// compare only few fields as the hierarchy fields(pointers, etc) are modified by the resolution function calls
		equals := func(t *testing.T, entry1, entry2 *model.ProcessCacheEntry) {
			t.Helper()

			assert.Equal(t, entry1.FileEvent.PathnameStr, entry2.FileEvent.PathnameStr)
			assert.Equal(t, entry1.Pid, entry2.Pid)
			assert.Equal(t, entry1.PPid, entry2.PPid)
			assert.Equal(t, entry1.ContainerID, entry2.ContainerID)
		}

		cacheEntry := resolvers.ProcessResolver.ResolveFromCache(pid, pid)
		if cacheEntry == nil {
			t.Errorf("not able to resolve the entry")
		}

		mapsEntry := resolvers.ProcessResolver.ResolveFromKernelMaps(pid, pid)
		if mapsEntry == nil {
			t.Errorf("not able to resolve the entry")
		}

		equals(t, cacheEntry, mapsEntry)

		procEntry := resolvers.ProcessResolver.ResolveFromProcfs(pid)
		if procEntry == nil {
			t.Errorf("not able to resolve the entry")
		}

		equals(t, cacheEntry, procEntry)

		if _, err = io.WriteString(stdin, "\n"); err != nil {
			t.Error(err)
		}
	})
}
