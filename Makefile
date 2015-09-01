linux:
	GOOS=linux GOARCH=amd64 go-bindata -pkg="main" -o amiinventory_bindata.go static/...
	GOOS=linux GOARCH=amd64 go build -o amiinventory amiinventory.go amiinventory_bindata.go

