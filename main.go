package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"

	"cloud.google.com/go/storage"
	"github.com/gorilla/mux"
	"github.com/olivere/elastic"
	"github.com/pborman/uuid"

	jwtmiddleware "github.com/auth0/go-jwt-middleware"
	jwt "github.com/dgrijalva/jwt-go"
)

const (
	POST_INDEX = "post"
	DISTANCE   = "200km"

	// internal url of current vm, at which elastic search installed
	ES_URL      = "http://10.128.0.2:9200"
	BUCKET_NAME = "jiawei-bucket"
)

// define the hashmap for mapping file postfix to actual type
var (
	mediaTypes = map[string]string{
		".jpeg": "image",
		".jpg":  "image",
		".gif":  "image",
		".png":  "image",
		".mov":  "video",
		".mp4":  "video",
		".avi":  "video",
		".flv":  "video",
		".wmv":  "video",
	}
)

type Location struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type Post struct {
	// `json:"user" is for the json parsing of the User field, by default is 'User'.
	User     string   `json:"user"`
	Message  string   `json:"message"`
	Location Location `json:"location"`
	Url      string   `json:"url"`
	Type     string   `json:"type"`
	Face     float32  `json:"face"`
}

func main() {
	fmt.Println("started-service")

	// initialize the jwtMiddleWare
	jwtMiddleware := jwtmiddleware.New(jwtmiddleware.Options{
		ValidationKeyGetter: func(token *jwt.Token) (interface{}, error) {
			return []byte(mySigningKey), nil
		},
		SigningMethod: jwt.SigningMethodHS256,
	})

	// use external library to set a http router
	r := mux.NewRouter()
	// bind handler functions which need to verify token
	r.Handle("/post", jwtMiddleware.Handler(http.HandlerFunc(handlerPost))).Methods("POST", "OPTIONS")
	r.Handle("/search", jwtMiddleware.Handler(http.HandlerFunc(handlerSearch))).Methods("GET", "OPTIONS")
	r.Handle("/cluster", jwtMiddleware.Handler(http.HandlerFunc(handlerCluster))).Methods("GET", "OPTIONS")
	// bind handler functions which DON'T need to verify token
	r.Handle("/signup", http.HandlerFunc(handlerSignup)).Methods("POST", "OPTIONS")
	r.Handle("/login", http.HandlerFunc(handlerLogin)).Methods("POST", "OPTIONS")

	/*http.HandleFunc("/post", handlerPost)
	http.HandleFunc("/cluster", handlerCluster)
	http.HandleFunc("/search", handlerSearch)
	log.Fatal(http.ListenAndServe(":8080", nil))*/

	log.Fatal(http.ListenAndServe(":8080", r))
}

/**
 * handle function for /post
 */
func handlerPost(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one post request")
	// set header
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
	if r.Method == "OPTIONS" {
		return
	}

	/*
	 * 1. read parameters from request body and token
	 */
	// retrieve the value of "lat" and "lon" key in reqeust body
	lat, _ := strconv.ParseFloat(r.FormValue("lat"), 64)
	lon, _ := strconv.ParseFloat(r.FormValue("lon"), 64)

	// retrieve username from token
	token := r.Context().Value("user")
	token_payload := token.(*jwt.Token).Claims
	username := token_payload.(jwt.MapClaims)["username"]

	// form the Post struct using parameters
	p := &Post{
		User:    username.(string),
		Message: r.FormValue("message"),
		Location: Location{
			Lat: lat,
			Lon: lon,
		},
	}

	/*
	 * 2. save image to Google Cloud Storage
	 */
	// retrieve the value of "image" key in request body
	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "Image is not available", http.StatusBadRequest)
		fmt.Printf("Image is not available %v\n", err)
		return
	}

	// get the postfix of the file using the file header in request
	suffix := filepath.Ext(header.Filename)
	// search in hashmap and get the type of post
	if t, ok := mediaTypes[suffix]; ok {
		p.Type = t
	} else {
		p.Type = "unknown"
	}

	// get a unique new id, and set that to be the id of the post
	id := uuid.New()

	// save image to GCS, and get the url of that image in GCS
	mediaLink, err := saveToGCS(file, id)
	if err != nil {
		http.Error(w, "Failed to save image to GCS", http.StatusInternalServerError)
		fmt.Printf("Failed to save image to GCS %v\n", err)
		return
	}

	// set the post url field to be the corresponding image url(external url) in GCS
	p.Url = mediaLink

	/*
	 * 3. annotate the image using Cloud Vision API
	 */
	// only image type can be check by vision api
	if p.Type == "image" {
		// form the internal uri based on the format of uri(internal uri) in GCS
		uri := fmt.Sprintf("gs://%s/%s", BUCKET_NAME, id)
		// call function to get the score of the image
		if score, err := annotate(uri); err != nil {
			http.Error(w, "Failed to annotate image", http.StatusInternalServerError)
			fmt.Printf("Failed to annotate the image %v\n", err)
			return
		} else {
			p.Face = score
		}
	}

	/*
	 * 4. save post to Elastic Search
	 */
	err = saveToES(p, POST_INDEX, id)
	if err != nil {
		http.Error(w, "Failed to save post to Elastic Search", http.StatusInternalServerError)
		fmt.Printf("Failed to save post to Elastic Search %v\n", err)

	}

	fmt.Println("post is saved to Elastic Search")
}

/**
 * handle function for /cluster?term=face
 * search for posts which the possibility of having faces is larger than 0.9
 */
func handlerCluster(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one cluster request")
	// set header
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
	if r.Method == "OPTIONS" {
		return
	}

	// get parameters form url
	term := r.URL.Query().Get("term")
	// set query to be face value > 0.9
	query := elastic.NewRangeQuery(term).Gte(0.9)

	// get the search results
	searchResult, err := readFromES(query, POST_INDEX)
	if err != nil {
		http.Error(w, "Failed to read from Elasticsearch", http.StatusInternalServerError)
		return
	}
	// get the posts from search results
	posts := getPostFromSearchResult(searchResult)
	js, err := json.Marshal(posts)
	if err != nil {
		http.Error(w, "Failed to parse post object", http.StatusInternalServerError)
		fmt.Printf("Failed to parse post object %v\n", err)
		return
	}
	w.Write(js)
}

/**
 * handle function for /search
 */
func handlerSearch(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one request for search")

	// set header
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
	if r.Method == "OPTIONS" {
		return
	}

	// get the paramters from URL since /search is get request
	// 64 means a 64 bit float64
	lat, _ := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, _ := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)

	// range is optional, if url doesn't have such parameter, use default DISTANCE
	ran := DISTANCE
	if val := r.URL.Query().Get("range"); val != "" {
		ran = val + "km"
	}
	fmt.Println("range is", ran)

	// generate database search query
	query := elastic.NewGeoDistanceQuery("location")
	query = query.Distance(ran).Lat(lat).Lon(lon)
	searchResult, err := readFromES(query, POST_INDEX)
	if err != nil {
		http.Error(w, "Failed to read from Elasticsearch", http.StatusInternalServerError)
		fmt.Printf("Failed to read post from Elasticsearch %v.\n", err)
		return
	}

	// get array of Post struct based on search result
	posts := getPostFromSearchResult(searchResult)

	// transform Post structs to json string
	js, err := json.Marshal(posts)
	if err != nil {
		http.Error(w, "Failed to parse posts  into JSON format", http.StatusInternalServerError)
		fmt.Printf("Failed to parse posts into JSON format %v.\n", err)
		return
	}

	// write json to http response body
	w.Write(js)
}

/**
 * return the search result based on
 * query, index number
 */
func readFromES(query elastic.Query, index string) (*elastic.SearchResult, error) {
	client, err := elastic.NewClient(elastic.SetURL(ES_URL))
	if err != nil {
		return nil, err
	}

	// obtain the search result based on the query
	searchResult, err := client.Search().
		Index(index).
		Query(query).
		Pretty(true).
		Do(context.Background())
	if err != nil {
		return nil, err
	}

	return searchResult, nil
}

/**
 * return the array of POST struct based on
 * the search result
 */
func getPostFromSearchResult(searchResult *elastic.SearchResult) []Post {
	var posts []Post

	var posttype Post
	// iterate through search result and check if the result item is in post struct
	for _, item := range searchResult.Each(reflect.TypeOf(posttype)) {
		// casting to Post struct
		p := item.(Post)
		posts = append(posts, p)
	}
	return posts
}

/**
 * r is the reader for source file
 * objectName is the name that you want to give to that file in GCS
 * This function stores file "r" to GCS using name "objectName"
 * return the external url of the file in GCS and error(nil for no error)
 */
func saveToGCS(r io.Reader, objectName string) (string, error) {
	// mandatory parameter
	ctx := context.Background()

	client, err := storage.NewClient(ctx)
	if err != nil {
		return "", err
	}

	// retrieve the metadata of the bucket to check if the bucket exist
	bucket := client.Bucket(BUCKET_NAME)
	// call bucket.Attrs(ctx) to check whether the attribute of bucket exist
	// which could implicitly check whether the bucket exist
	if _, err := bucket.Attrs(ctx); err != nil {
		return "", err
	}

	// create an object which will be stored in GCS
	object := bucket.Object(objectName)
	// create the writer corresponding to that object
	wc := object.NewWriter(ctx)
	// copy the source file r to writer wc, and writer write to object
	if _, err := io.Copy(wc, r); err != nil {
		return "", err
	}
	// close the writer
	if err := wc.Close(); err != nil {
		return "", err
	}

	// set the read access control to all users
	// so that the front-end can use the external url to read the image file in GCS
	if err := object.ACL().Set(ctx, storage.AllUsers, storage.RoleReader); err != nil {
		return "", err
	}

	// call Attrs to get the attribute of that file
	// so that retrieve the url of that file in GCS
	attrs, err := object.Attrs(ctx)
	if err != nil {
		return "", err
	}

	fmt.Printf("image is saved to GCS: %s\n", attrs.MediaLink)
	return attrs.MediaLink, nil
}

/**
 * store the post body, index and id info to Elastic Search
 * if error occurs, return error
 */
func saveToES(i interface{}, index string, id string) error {
	client, err := elastic.NewClient(elastic.SetURL(ES_URL))
	if err != nil {
		return err
	}

	// .Index() is the same process as Insert in MySQL
	_, err = client.Index().
		Index(index).
		Id(id).
		BodyJson(i).
		Do(context.Background())

	if err != nil {
		return err
	}

	fmt.Printf("One record is saved to index: %s\n", index)
	return nil
}
