//go:build integration

package mongomigrate_test

import "go.mongodb.org/mongo-driver/v2/mongo/options"

func optionsUpsert() *options.UpdateOneOptionsBuilder { return options.UpdateOne().SetUpsert(true) }
