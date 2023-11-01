harhar
======

HTTP Archive (HAR) recording for Go code using the http.RoundTripper interface. 

Getting Started
---------------

For logging from an `http.Client` you can simply set the Transport property:

```go
	recorder := harhar.NewRecorder(http.DefaultTransport) 
	client := &http.Client{
		Transport: recorder,
	}
```

Then, whenever you're ready to generate the HAR output, call WriteFile:

	recorder.WriteFile("output.har")
