package main

import (
	"context"
	"fmt"

	// vision is the alias of the import package
	vision "cloud.google.com/go/vision/apiv1"
)

/**
 * Annotate an image file based on Cloud Vision API,
 * parameters : uri of the image file in GCS
 * return score and error if exists
 */
func annotate(uri string) (float32, error) {
	ctx := context.Background()

	client, err := vision.NewImageAnnotatorClient(ctx)
	if err != nil {
		return 0.0, err
	}
	defer client.Close()

	// read image file from uri
	image := vision.NewImageFromURI(uri)
	// detect face number up limit 1
	faceAnnotations, err := client.DetectFaces(ctx, image, nil, 1)
	if err != nil {
		return 0.0, err
	}

	if len(faceAnnotations) == 0 {
		fmt.Println("No faces found")
		return 0.0, nil
	}

	// maximum one face result will be found, therefore get 0th of the array
	// based on the json structure of the response body
	return faceAnnotations[0].DetectionConfidence, nil
}
