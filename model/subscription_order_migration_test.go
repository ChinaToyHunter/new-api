package model

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// subscriptionOrderMigrationLegacy mirrors the SubscriptionOrder schema from the
// latest released version before Stripe subscription session binding was added.
type subscriptionOrderMigrationLegacy struct {
	Id                         int     `json:"id"`
	UserId                     int     `json:"user_id" gorm:"index"`
	PlanId                     int     `json:"plan_id" gorm:"index"`
	Money                      float64 `json:"money"`
	PaymentExpectationVersion  int     `json:"-" gorm:"default:0"`
	ExpectedAmount             float64 `json:"-"`
	ExpectedCurrency           string  `json:"-" gorm:"type:varchar(16);default:''"`
	ExpectedStoreID            string  `json:"-" gorm:"type:varchar(255);default:''"`
	EntitlementSnapshotVersion int     `json:"-" gorm:"default:0"`
	EntitlementSnapshot        string  `json:"-" gorm:"type:text"`
	TradeNo                    string  `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod              string  `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider            string  `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	Status                     string  `json:"status"`
	CreateTime                 int64   `json:"create_time"`
	CompleteTime               int64   `json:"complete_time"`
	ProviderPayload            string  `json:"provider_payload" gorm:"type:text"`
}

func testSubscriptionOrderFreshMigration(t *testing.T, db *gorm.DB, recorder *migrationSQLRecorder) {
	t.Helper()
	tableName := fmt.Sprintf("subscription_order_fresh_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Migrator().DropTable(tableName) })
	tableDB := db.Table(tableName)

	require.NoError(t, tableDB.AutoMigrate(&SubscriptionOrder{}))
	secureOrder := &SubscriptionOrder{
		UserId:                     701,
		PlanId:                     31,
		Money:                      9.99,
		PaymentExpectationVersion:  StripeSubscriptionPaymentExpectationVersion,
		ExpectedAmount:             9.99,
		ExpectedAmountUnit:         999,
		ExpectedCurrency:           "USD",
		ExpectedSessionID:          "cs_fresh",
		ExpectedBindingToken:       "fresh-binding",
		ExpectedStoreID:            "",
		EntitlementSnapshotVersion: SubscriptionEntitlementSnapshotVersion,
		EntitlementSnapshot:        `{"plan_title":"Fresh","duration_unit":"month"}`,
		TradeNo:                    "sub-migration-fresh",
		PaymentMethod:              PaymentMethodStripe,
		PaymentProvider:            PaymentProviderStripe,
		Status:                     "pending",
		CreateTime:                 100,
	}
	require.NoError(t, tableDB.Create(secureOrder).Error)

	var stored SubscriptionOrder
	require.NoError(t, tableDB.Where("trade_no = ?", secureOrder.TradeNo).First(&stored).Error)
	assert.Equal(t, int64(999), stored.ExpectedAmountUnit)
	assert.Equal(t, "cs_fresh", stored.ExpectedSessionID)
	assert.Equal(t, "fresh-binding", stored.ExpectedBindingToken)

	recorder.reset()
	require.NoError(t, tableDB.AutoMigrate(&SubscriptionOrder{}))
	assert.Empty(t, recorder.schemaMutations(), "a repeated fresh migration must not repeat schema DDL")
}

func testSubscriptionOrderLegacyUpgrade(t *testing.T, db *gorm.DB, recorder *migrationSQLRecorder) {
	t.Helper()
	tableName := fmt.Sprintf("subscription_order_upgrade_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Migrator().DropTable(tableName) })
	tableDB := db.Table(tableName)

	legacy := &subscriptionOrderMigrationLegacy{
		UserId:                     702,
		PlanId:                     32,
		Money:                      12.5,
		PaymentExpectationVersion:  0,
		ExpectedAmount:             12.5,
		ExpectedCurrency:           "USD",
		ExpectedStoreID:            "",
		EntitlementSnapshotVersion: 0,
		EntitlementSnapshot:        "",
		TradeNo:                    "sub-migration-legacy",
		PaymentMethod:              PaymentMethodStripe,
		PaymentProvider:            PaymentProviderStripe,
		Status:                     "pending",
		CreateTime:                 200,
		ProviderPayload:            "legacy-payload",
	}
	require.NoError(t, tableDB.AutoMigrate(&subscriptionOrderMigrationLegacy{}))
	require.NoError(t, tableDB.Create(legacy).Error)

	require.NoError(t, tableDB.AutoMigrate(&SubscriptionOrder{}))
	var migrated SubscriptionOrder
	require.NoError(t, tableDB.Where("trade_no = ?", legacy.TradeNo).First(&migrated).Error)
	assert.Equal(t, legacy.UserId, migrated.UserId)
	assert.Equal(t, legacy.PlanId, migrated.PlanId)
	assert.Equal(t, legacy.Money, migrated.Money)
	assert.Equal(t, legacy.ProviderPayload, migrated.ProviderPayload)
	assert.Zero(t, migrated.ExpectedAmountUnit, "new minor-unit expectation must default safely")
	assert.Empty(t, migrated.ExpectedSessionID, "new session binding must default safely")
	assert.Empty(t, migrated.ExpectedBindingToken, "new binding token must default safely")

	columnTypes, err := tableDB.Migrator().ColumnTypes(&SubscriptionOrder{})
	require.NoError(t, err)
	columns := make(map[string]struct{}, len(columnTypes))
	for _, columnType := range columnTypes {
		columns[strings.ToLower(columnType.Name())] = struct{}{}
	}
	for _, column := range []string{"expected_amount_unit", "expected_session_id", "expected_binding_token"} {
		_, found := columns[column]
		assert.True(t, found, "migrated schema must contain %s", column)
	}

	recorder.reset()
	require.NoError(t, tableDB.AutoMigrate(&SubscriptionOrder{}))
	assert.Empty(t, recorder.schemaMutations(), "a repeated upgrade migration must not repeat schema DDL")
}

func testSubscriptionOrderMigration(t *testing.T, db *gorm.DB, recorder *migrationSQLRecorder) {
	t.Helper()
	testSubscriptionOrderFreshMigration(t, db, recorder)
	testSubscriptionOrderLegacyUpgrade(t, db, recorder)
}

func TestSubscriptionOrderMigrationSQLite(t *testing.T) {
	recorder := &migrationSQLRecorder{}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: recorder})
	require.NoError(t, err)
	testSubscriptionOrderMigration(t, db, recorder)
}

func TestSubscriptionOrderMigrationConfiguredDatabases(t *testing.T) {
	tests := []struct {
		name      string
		env       string
		dialector func(string) gorm.Dialector
	}{
		{name: "mysql", env: "TEST_MYSQL_DSN", dialector: func(dsn string) gorm.Dialector { return mysql.Open(dsn) }},
		{name: "postgres", env: "TEST_POSTGRES_DSN", dialector: func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(test.env))
			if dsn == "" {
				t.Skip(test.env + " is not configured")
			}
			recorder := &migrationSQLRecorder{}
			db, err := gorm.Open(test.dialector(dsn), &gorm.Config{Logger: recorder})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			testSubscriptionOrderMigration(t, db, recorder)
		})
	}
}
