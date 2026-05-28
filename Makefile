BINARY := tcl-script-runner
CMD     := ./cmd

.PHONY: build build-windows run clean

build:
	go build -o $(BINARY) $(CMD)

build-windows:
	GOOS=windows GOARCH=amd64 go build -o $(BINARY).exe $(CMD)

run: build
	./$(BINARY) --listen :8080

clean:
	rm -f $(BINARY) $(BINARY).exe
