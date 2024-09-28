package main

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
)

type User struct {
	Username string `bson:"_id"`
}

func getUserByToken(token string) (User, error) {
	var user User
	err := db.Collection("usersv0").FindOne(context.TODO(), bson.M{"tokens": token}).Decode(&user)
	return user, err
}
