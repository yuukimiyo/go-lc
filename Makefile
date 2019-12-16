#Makefile
# yuuki.miyo@gmail.com

NAME     := $(shell basename `pwd`)
BINDIR   := bin
REPO     := github.com/yuukimiyo/$(NAME)
VERSION  := v0.1

arg	:=

.PHONY: clean
clean:
	rm -f $(BINDIR)/*
	rm -fr vendor
	rm -f Gopkg.*

.PHONY: init
init: clean
	dep init

.PHONY: ensure
ensure:
	dep ensure

.PHONY: build
build:
	go build -o $(BINDIR)/$(NAME)

.PHONY: run
run:
	$(BINDIR)/$(NAME) $(arg)

.PHONY: dev
dev:
	@go build -o $(BINDIR)/$(NAME)
	$(BINDIR)/$(NAME) $(arg)
