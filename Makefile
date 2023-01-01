test:
	@go test \
		-count=1 \
		-short \
		-shuffle=on \
		-timeout=5m \
		./...
.PHONY: test

test-acc:
	@go test \
		-count=1 \
		-race \
		-shuffle=on \
		-timeout=10m \
		./...
.PHONY: test-acc
