APP_NAME := bff-hang
ZIP_NAME := function.zip
BOOTSTRAP := bootstrap
GOOS := linux
GOARCH := arm64

build-lambda:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BOOTSTRAP)

package-lambda: build-lambda
	zip $(ZIP_NAME) $(BOOTSTRAP)

deploy: package-lambda
	cd terraform && terraform init && terraform apply -var="lambda_package_path=../$(ZIP_NAME)"

clean:
	rm -f $(BOOTSTRAP) $(ZIP_NAME)
