.PHONY: build test prepare smoke package clean

build:
	./build.sh build

test:
	./build.sh test

prepare:
	./build.sh prepare

# Uses a native build; cross-compiled binaries cannot run on the build host.
smoke:
	python3 tests/smoke.py "$${AGENT_OUTPUT:-$(CURDIR)/bin/picoclaw}"

package:
	python3 package.py

clean:
	rm -rf bin
