package service

import "github.com/Wei-Shaw/sub2api/internal/config"

func ProvideOpenAIGatewayService(
	accountRepo AccountRepository,
	usageLogRepo UsageLogRepository,
	usageBillingRepo UsageBillingRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache GatewayCache,
	cfg *config.Config,
	schedulerSnapshot *SchedulerSnapshotService,
	concurrencyService *ConcurrencyService,
	billingService *BillingService,
	rateLimitService *RateLimitService,
	billingCacheService *BillingCacheService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	openAITokenProvider *OpenAITokenProvider,
	grokTokenProvider *GrokTokenProvider,
	resolver *ModelPricingResolver,
	channelService *ChannelService,
	balanceNotifyService *BalanceNotifyService,
	settingService *SettingService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
	encryptor SecretEncryptor,
) *OpenAIGatewayService {
	svc := NewOpenAIGatewayService(accountRepo, usageLogRepo, usageBillingRepo, userRepo, userSubRepo, userGroupRateRepo, cache, cfg, schedulerSnapshot, concurrencyService, billingService, rateLimitService, billingCacheService, httpUpstream, deferredService, openAITokenProvider, grokTokenProvider, resolver, channelService, balanceNotifyService, settingService, userPlatformQuotaRepo)
	svc.turnStateEncryptor = encryptor
	return svc
}
