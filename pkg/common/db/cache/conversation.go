// Copyright © 2023 OpenIM. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cache

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/dtm-labs/rockscache"
	"github.com/openimsdk/open-im-server/v3/pkg/common/cachekey"
	"github.com/openimsdk/open-im-server/v3/pkg/common/config"
	relationtb "github.com/openimsdk/open-im-server/v3/pkg/common/db/table/relation"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/utils/datautil"
	"github.com/openimsdk/tools/utils/encrypt"
	"github.com/redis/go-redis/v9"
)

const (
	// ConversationKey                          = "CONVERSATION:"
	// conversationIDsKey                       = "CONVERSATION_IDS:"
	// conversationIDsHashKey                   = "CONVERSATION_IDS_HASH:"
	// conversationHasReadSeqKey                = "CONVERSATION_HAS_READ_SEQ:"
	// recvMsgOptKey                            = "RECV_MSG_OPT:"
	// superGroupRecvMsgNotNotifyUserIDsKey     = "SUPER_GROUP_RECV_MSG_NOT_NOTIFY_USER_IDS:"
	// superGroupRecvMsgNotNotifyUserIDsHashKey = "SUPER_GROUP_RECV_MSG_NOT_NOTIFY_USER_IDS_HASH:"
	// conversationNotReceiveMessageUserIDsKey  = "CONVERSATION_NOT_RECEIVE_MESSAGE_USER_IDS:".

	conversationExpireTime = time.Second * 60 * 60 * 12
)

// arg fn will exec when no data in msgCache.
type ConversationCache interface {
	metaCache
	NewCache() ConversationCache
	// get user's conversationIDs from msgCache
	GetUserConversationIDs(ctx context.Context, ownerUserID string) ([]string, error)
	DelConversationIDs(userIDs ...string) ConversationCache

	GetUserConversationIDsHash(ctx context.Context, ownerUserID string) (hash uint64, err error)
	DelUserConversationIDsHash(ownerUserIDs ...string) ConversationCache

	// get one conversation from msgCache
	GetConversation(ctx context.Context, ownerUserID, conversationID string) (*relationtb.ConversationModel, error)
	DelConversations(ownerUserID string, conversationIDs ...string) ConversationCache
	DelUsersConversation(conversationID string, ownerUserIDs ...string) ConversationCache
	// get one conversation from msgCache
	GetConversations(ctx context.Context, ownerUserID string,
		conversationIDs []string) ([]*relationtb.ConversationModel, error)
	// get one user's all conversations from msgCache
	GetUserAllConversations(ctx context.Context, ownerUserID string) ([]*relationtb.ConversationModel, error)
	// get user conversation recv msg from msgCache
	GetUserRecvMsgOpt(ctx context.Context, ownerUserID, conversationID string) (opt int, err error)
	DelUserRecvMsgOpt(ownerUserID, conversationID string) ConversationCache
	// get one super group recv msg but do not notification userID list
	// GetSuperGroupRecvMsgNotNotifyUserIDs(ctx context.Context, groupID string) (userIDs []string, err error)
	DelSuperGroupRecvMsgNotNotifyUserIDs(groupID string) ConversationCache
	// get one super group recv msg but do not notification userID list hash
	// GetSuperGroupRecvMsgNotNotifyUserIDsHash(ctx context.Context, groupID string) (hash uint64, err error)
	DelSuperGroupRecvMsgNotNotifyUserIDsHash(groupID string) ConversationCache

	// GetUserAllHasReadSeqs(ctx context.Context, ownerUserID string) (map[string]int64, error)
	DelUserAllHasReadSeqs(ownerUserID string, conversationIDs ...string) ConversationCache

	GetConversationsByConversationID(ctx context.Context,
		conversationIDs []string) ([]*relationtb.ConversationModel, error)
	DelConversationByConversationID(conversationIDs ...string) ConversationCache
	GetConversationNotReceiveMessageUserIDs(ctx context.Context, conversationID string) ([]string, error)
	DelConversationNotReceiveMessageUserIDs(conversationIDs ...string) ConversationCache
}

// NewConversationRedis conversation cache使用了rockscahe保证数据一致性
func NewConversationRedis(rdb redis.UniversalClient, localCache *config.LocalCache, opts rockscache.Options, db relationtb.ConversationModelInterface) ConversationCache {
	rcClient := rockscache.NewClient(rdb, opts)
	// 设置rcClient关联的重试间隔/重试次数
	mc := NewMetaCacheRedis(rcClient)
	c := localCache.Conversation
	log.ZDebug(context.Background(), "black local cache init", "Topic", c.Topic, "SlotNum", c.SlotNum, "SlotSize", c.SlotSize, "enable", c.Enable())
	mc.SetTopic(c.Topic)
	// 设置常规redis client
	mc.SetRawRedisClient(rdb)
	return &ConversationRedisCache{
		rcClient:       rcClient,
		metaCache:      mc,
		conversationDB: db,
		expireTime:     conversationExpireTime, // redis conversation缓存12小时
	}
}

// 会话redis缓存使用rockscache保证数据一致性
type ConversationRedisCache struct {
	metaCache
	rcClient       *rockscache.Client
	conversationDB relationtb.ConversationModelInterface
	expireTime     time.Duration
}

// func NewNewConversationRedis(
//	rdb redis.UniversalClient,
//	conversationDB *relation.ConversationGorm,
//	options rockscache.Options,
// ) ConversationCache {
//	rcClient := rockscache.NewClient(rdb, options)
//
//	return &ConversationRedisCache{
//		rcClient:       rcClient,
//		metaCache:      NewMetaCacheRedis(rcClient),
//		conversationDB: conversationDB,
//		expireTime:     conversationExpireTime,
//	}
//}

func (c *ConversationRedisCache) NewCache() ConversationCache {
	return &ConversationRedisCache{
		rcClient:       c.rcClient,
		metaCache:      c.Copy(),
		conversationDB: c.conversationDB,
		expireTime:     c.expireTime,
	}
}

// conversation id和conversation发起人user id关系redis key
func (c *ConversationRedisCache) getConversationKey(ownerUserID, conversationID string) string {
	return cachekey.GetConversationKey(ownerUserID, conversationID)
}

// user id对应的所有conversation ids key
func (c *ConversationRedisCache) getConversationIDsKey(ownerUserID string) string {
	return cachekey.GetConversationIDsKey(ownerUserID)
}

func (c *ConversationRedisCache) getSuperGroupRecvNotNotifyUserIDsKey(groupID string) string {
	return cachekey.GetSuperGroupRecvNotNotifyUserIDsKey(groupID)
}

// user id与conversation消息接收条件选项key
func (c *ConversationRedisCache) getRecvMsgOptKey(ownerUserID, conversationID string) string {
	return cachekey.GetRecvMsgOptKey(ownerUserID, conversationID)
}

func (c *ConversationRedisCache) getSuperGroupRecvNotNotifyUserIDsHashKey(groupID string) string {
	return cachekey.GetSuperGroupRecvNotNotifyUserIDsHashKey(groupID)
}

// 用户在当前会话已读的seq key
func (c *ConversationRedisCache) getConversationHasReadSeqKey(ownerUserID, conversationID string) string {
	return cachekey.GetConversationHasReadSeqKey(ownerUserID, conversationID)
}

// 屏蔽接收当前会话的user ids key
func (c *ConversationRedisCache) getConversationNotReceiveMessageUserIDsKey(conversationID string) string {
	return cachekey.GetConversationNotReceiveMessageUserIDsKey(conversationID)
}

// 用户所有会话id的hash key, 用于比较是否变化？
func (c *ConversationRedisCache) getUserConversationIDsHashKey(ownerUserID string) string {
	return cachekey.GetUserConversationIDsHashKey(ownerUserID)
}

// rockscache获取用户所有的会话ids
func (c *ConversationRedisCache) GetUserConversationIDs(ctx context.Context, ownerUserID string) ([]string, error) {
	// 首先从redis中查找，没找到再从db中查询， 用的是rockscache
	return getCache(ctx, c.rcClient, c.getConversationIDsKey(ownerUserID), c.expireTime, func(ctx context.Context) ([]string, error) {
		// 从db中查询user id对对应的conversation ids
		return c.conversationDB.FindUserIDAllConversationID(ctx, ownerUserID)
	})
}

// DelConversationIDs 删除user ids对对应的conversation redis缓存
func (c *ConversationRedisCache) DelConversationIDs(userIDs ...string) ConversationCache {
	keys := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		keys = append(keys, c.getConversationIDsKey(userID))
	}
	cache := c.NewCache()
	cache.AddKeys(keys...)

	return cache
}

// rockscache获取用户所有的会话ids，并拼接计算hash值返回保存
func (c *ConversationRedisCache) GetUserConversationIDsHash(ctx context.Context, ownerUserID string) (hash uint64, err error) {
	// 先从redis查找，再从db查找，并写入redis
	return getCache(
		ctx,
		c.rcClient,
		c.getUserConversationIDsHashKey(ownerUserID),
		c.expireTime,
		func(ctx context.Context) (uint64, error) {
			conversationIDs, err := c.GetUserConversationIDs(ctx, ownerUserID)
			if err != nil {
				return 0, err
			}
			datautil.Sort(conversationIDs, true)
			bi := big.NewInt(0)
			bi.SetString(encrypt.Md5(strings.Join(conversationIDs, ";"))[0:8], 16)
			return bi.Uint64(), nil
		},
	)
}

func (c *ConversationRedisCache) DelUserConversationIDsHash(ownerUserIDs ...string) ConversationCache {
	keys := make([]string, 0, len(ownerUserIDs))
	for _, ownerUserID := range ownerUserIDs {
		keys = append(keys, c.getUserConversationIDsHashKey(ownerUserID))
	}
	cache := c.NewCache()
	cache.AddKeys(keys...)

	return cache
}

// rockscache查询用户指定会话信息
func (c *ConversationRedisCache) GetConversation(ctx context.Context, ownerUserID, conversationID string) (*relationtb.ConversationModel, error) {
	// 从redis中获取用户的当前会话内容
	return getCache(ctx, c.rcClient, c.getConversationKey(ownerUserID, conversationID), c.expireTime, func(ctx context.Context) (*relationtb.ConversationModel, error) {
		return c.conversationDB.Take(ctx, ownerUserID, conversationID)
	})
}

func (c *ConversationRedisCache) DelConversations(ownerUserID string, conversationIDs ...string) ConversationCache {
	keys := make([]string, 0, len(conversationIDs))
	for _, conversationID := range conversationIDs {
		keys = append(keys, c.getConversationKey(ownerUserID, conversationID))
	}
	cache := c.NewCache()
	cache.AddKeys(keys...)

	return cache
}

// func (c *ConversationRedisCache) getConversationIndex(convsation *relationtb.ConversationModel, keys []string) (int, error) {
// 	key := c.getConversationKey(convsation.OwnerUserID, convsation.ConversationID)
// 	for _i, _key := range keys {
// 		if _key == key {
// 			return _i, nil
// 		}
// 	}

//		return 0, errs.New("not found key:" + key + " in keys")
//	}
//
// rockscache循环获取用户多个会话的内容
func (c *ConversationRedisCache) GetConversations(ctx context.Context, ownerUserID string, conversationIDs []string) ([]*relationtb.ConversationModel, error) {
	// var keys []string
	// for _, conversarionID := range conversationIDs {
	//	keys = append(keys, c.getConversationKey(ownerUserID, conversarionID))
	//}
	// return batchGetCache(
	//	ctx,
	//	c.rcClient,
	//	keys,
	//	c.expireTime,
	//	c.getConversationIndex,
	//	func(ctx context.Context) ([]*relationtb.ConversationModel, error) {
	//		return c.conversationDB.Find(ctx, ownerUserID, conversationIDs)
	//	},
	//)
	return batchGetCache2(ctx, c.rcClient, c.expireTime, conversationIDs, func(conversationID string) string {
		return c.getConversationKey(ownerUserID, conversationID)
	}, func(ctx context.Context, conversationID string) (*relationtb.ConversationModel, error) {
		return c.conversationDB.Take(ctx, ownerUserID, conversationID)
	})
}

// rockscache获取用户所有会话消息
func (c *ConversationRedisCache) GetUserAllConversations(ctx context.Context, ownerUserID string) ([]*relationtb.ConversationModel, error) {
	conversationIDs, err := c.GetUserConversationIDs(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	// var keys []string
	// for _, conversarionID := range conversationIDs {
	//	keys = append(keys, c.getConversationKey(ownerUserID, conversarionID))
	//}
	// return batchGetCache(
	//	ctx,
	//	c.rcClient,
	//	keys,
	//	c.expireTime,
	//	c.getConversationIndex,
	//	func(ctx context.Context) ([]*relationtb.ConversationModel, error) {
	//		return c.conversationDB.FindUserIDAllConversations(ctx, ownerUserID)
	//	},
	//)
	return c.GetConversations(ctx, ownerUserID, conversationIDs)
}

// rockscache获取用户当前会话设置的消息接收选项设置
func (c *ConversationRedisCache) GetUserRecvMsgOpt(ctx context.Context, ownerUserID, conversationID string) (opt int, err error) {
	return getCache(ctx, c.rcClient, c.getRecvMsgOptKey(ownerUserID, conversationID), c.expireTime, func(ctx context.Context) (opt int, err error) {
		return c.conversationDB.GetUserRecvMsgOpt(ctx, ownerUserID, conversationID)
	})
}

// func (c *ConversationRedisCache) GetSuperGroupRecvMsgNotNotifyUserIDs(ctx context.Context, groupID string) (userIDs []string, err error) {
//	return getCache(ctx, c.rcClient, c.getSuperGroupRecvNotNotifyUserIDsKey(groupID), c.expireTime, func(ctx context.Context) (userIDs []string, err error) {
//		return c.conversationDB.FindSuperGroupRecvMsgNotNotifyUserIDs(ctx, groupID)
//	})
//}

func (c *ConversationRedisCache) DelUsersConversation(conversationID string, ownerUserIDs ...string) ConversationCache {
	keys := make([]string, 0, len(ownerUserIDs))
	for _, ownerUserID := range ownerUserIDs {
		keys = append(keys, c.getConversationKey(ownerUserID, conversationID))
	}
	cache := c.NewCache()
	cache.AddKeys(keys...)

	return cache
}

func (c *ConversationRedisCache) DelUserRecvMsgOpt(ownerUserID, conversationID string) ConversationCache {
	cache := c.NewCache()
	cache.AddKeys(c.getRecvMsgOptKey(ownerUserID, conversationID))

	return cache
}

func (c *ConversationRedisCache) DelSuperGroupRecvMsgNotNotifyUserIDs(groupID string) ConversationCache {
	cache := c.NewCache()
	cache.AddKeys(c.getSuperGroupRecvNotNotifyUserIDsKey(groupID))

	return cache
}

// func (c *ConversationRedisCache) GetSuperGroupRecvMsgNotNotifyUserIDsHash(ctx context.Context, groupID string) (hash uint64, err error) {
//	return getCache(ctx, c.rcClient, c.getSuperGroupRecvNotNotifyUserIDsHashKey(groupID), c.expireTime, func(ctx context.Context) (hash uint64, err error) {
//		userIDs, err := c.GetSuperGroupRecvMsgNotNotifyUserIDs(ctx, groupID)
//		if err != nil {
//			return 0, err
//		}
//		utils.Sort(userIDs, true)
//		bi := big.NewInt(0)
//		bi.SetString(utils.Md5(strings.Join(userIDs, ";"))[0:8], 16)
//		return bi.Uint64(), nil
//	},
//	)
//}

func (c *ConversationRedisCache) DelSuperGroupRecvMsgNotNotifyUserIDsHash(groupID string) ConversationCache {
	cache := c.NewCache()
	cache.AddKeys(c.getSuperGroupRecvNotNotifyUserIDsHashKey(groupID))

	return cache
}

func (c *ConversationRedisCache) DelUserAllHasReadSeqs(ownerUserID string, conversationIDs ...string) ConversationCache {
	cache := c.NewCache()
	for _, conversationID := range conversationIDs {
		cache.AddKeys(c.getConversationHasReadSeqKey(ownerUserID, conversationID))
	}

	return cache
}

func (c *ConversationRedisCache) GetConversationsByConversationID(ctx context.Context, conversationIDs []string) ([]*relationtb.ConversationModel, error) {
	panic("implement me")
}

func (c *ConversationRedisCache) DelConversationByConversationID(conversationIDs ...string) ConversationCache {
	panic("implement me")
}

// rockscache获取当前会话不接收消息的用户ids
func (c *ConversationRedisCache) GetConversationNotReceiveMessageUserIDs(ctx context.Context, conversationID string) ([]string, error) {
	return getCache(ctx, c.rcClient, c.getConversationNotReceiveMessageUserIDsKey(conversationID), c.expireTime, func(ctx context.Context) ([]string, error) {
		return c.conversationDB.GetConversationNotReceiveMessageUserIDs(ctx, conversationID)
	})
}

func (c *ConversationRedisCache) DelConversationNotReceiveMessageUserIDs(conversationIDs ...string) ConversationCache {
	cache := c.NewCache()
	for _, conversationID := range conversationIDs {
		cache.AddKeys(c.getConversationNotReceiveMessageUserIDsKey(conversationID))
	}

	return cache
}
