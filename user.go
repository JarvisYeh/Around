package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"time"

	jwt "github.com/dgrijalva/jwt-go"
	"github.com/olivere/elastic"
)

const (
	USER_INDEX = "user"
)

type User struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Age      int64  `json:"age"`
	Gender   string `json:"gender"`
}

var mySigningKey = []byte("secret")

/* helper function to check whether the user index in elastic search contains one record
 * with "username" = username, "password" = password
 * if exist return true, nil
 * if doesn't exist return false, nil
 * if facing some error, return false, error
 */
func checkUser(username, password string) (bool, error) {
	// create query which filter out record username = param.username
	query := elastic.NewTermQuery("username", username)
	// get the result records from user index
	searchResult, err := readFromES(query, USER_INDEX)
	if err != nil {
		return false, err
	}

	// iterate through each record check if there is one with  password match
	var utype User
	for _, item := range searchResult.Each(reflect.TypeOf(utype)) {
		// cast item to User struct
		u := item.(User)
		if u.Password == password {
			return true, nil
		}
	}

	// that user is not find in index
	return false, nil
}

/*
 * helper function to add a user record into elastic search user index
 * if added successful, return true, nil
 * if user with that username already exist, return false, nil
 * if facing some error, return false, error
 */
func addUser(user *User) (bool, error) {
	/* check if the user with that username is already exist */
	// create query which filter out record username = param.username
	query := elastic.NewTermQuery("username", user.Username)
	// get the result records from user index
	searchResult, err := readFromES(query, USER_INDEX)
	if err != nil {
		return false, err
	}
	// if records amount > 0, that user is already in user index
	if searchResult.TotalHits() > 0 {
		return false, nil
	}

	// else, add that user to user index
	err = saveToES(user, USER_INDEX, user.Username)
	if err != nil {
		return false, err
	}
	fmt.Printf("User is added: %s/n", user.Username)
	return true, nil
}

/*
 * Http handler function for /login
 */
func handlerLogin(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one login request")
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method == "OPTIONS" {
		return
	}

	// decode the username and password from request json body
	decoder := json.NewDecoder(r.Body)
	var user User
	if err := decoder.Decode(&user); err != nil {
		http.Error(w, "Failed to read user data", http.StatusBadRequest)
		return
	}

	// check if that user exist in elastic search user index
	exists, err := checkUser(user.Username, user.Password)
	// error occurs while check for existence
	if err != nil {
		// return status code 500
		http.Error(w, "Failed to read user data from ElasticSearch", http.StatusInternalServerError)
		return
	}
	// user with that username and password doesn't exist in elastic search user index
	if !exists {
		// return status code 401
		http.Error(w, "User doesn't exist or wrong password", http.StatusUnauthorized)
		return
	}

	// user jwt-go library to generate the token
	// clarify body first
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"username": user.Username,
		"exp":      time.Now().Add(time.Hour * 24).Unix(),
	})
	// encrypt with defined key
	tokenString, err := token.SignedString(mySigningKey)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		fmt.Printf("Failed to generate token %v\n", err)
		return
	}
	w.Write([]byte(tokenString))
}

/*
 * Http handler function for /signup
 */
func handlerSignup(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one sign up request")
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method == "OPTIONS" {
		return
	}

	// decode request json boby in to User contruct
	decoder := json.NewDecoder(r.Body)
	var user User
	if err := decoder.Decode(&user); err != nil {
		http.Error(w, "Cannot decode user data from client", http.StatusBadRequest)
		fmt.Printf("Cannot decode user data from client %v\n", err)
		return
	}

	// check if the input user name and password is valid
	// here we check not empty username and pssword
	// and user name has to be consist by a-z, 0-9
	if user.Username == "" || user.Password == "" || regexp.MustCompile(`^[a-z0-9]$`).MatchString(user.Username) {
		http.Error(w, "Invalid username or password", http.StatusBadRequest)
		fmt.Printf("Invalid username or password\n")
		return
	}

	// if valid, add that User into elastic search user index (add into database as register process)
	success, err := addUser(&user)
	if err != nil {
		http.Error(w, "Failed to save user to Elasticsearch", http.StatusInternalServerError)
		fmt.Printf("Failed to save user to Elasticsearch %v\n", err)
		return
	}
	if !success {
		http.Error(w, "User already exists", http.StatusBadRequest)
		fmt.Println("User already exists")
		return
	}
	fmt.Printf("User added successfully: %s.\n", user.Username)
}
