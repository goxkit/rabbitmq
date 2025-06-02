install:
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@go install github.com/securego/gosec/v2/cmd/gosec@latest
	@curl -sfL https://raw.githubusercontent.com/securego/gosec/master/install.sh | sudo sh -s -- -b /bin v2.19.0

download:
	@echo "Downloading external packages..."
	go mod download
	@echo "External packages downloaded successfully!"

update:
	@echo "Updating external packages..."
	go get -u all && go mod tidy
	@echo "External packages updated successfully!"

tests:
	@echo "Running unit tests..."
	go test ./... -v -covermode atomic -coverprofile=coverage.out
	@echo "All unit test runned successfully!"

lint:
	@echo "Running golangci-lint..."
	golangci-lint run --print-issued-lines=false --print-linter-name=false --issues-exit-code=0 --enable=revive -- ./...

gosec:
	gosec -quiet ./...

push: lint gosec
	git push

test-cov:
	@go test ./... -v -covermode atomic -coverprofile=coverage.out
