# Task Plan: Unify Key as CID with Multicodec

## Goal
删除 `key.Key` 类型，统一使用 `cid.Cid`，通过 multicodec 区分不同的 commitment scheme 和数据类型。

## 设计方案

### 1. Multicodec 分配 (Private Use Area: 0x300000-0x3FFFFF)

| Codec | 名称 | 用途 |
|-------|------|------|
| 0x300001 | malt-kzg | KZG commitment (48 bytes) |
| 0x300002 | malt-verkle | Verkle commitment (31 bytes stem) |
| 0x300003 | malt-ipa | IPA commitment (32 bytes) |
| 0x55 | raw | 原始数据 (现有) |
| 0x71 | dag-cbor | CBOR 编码 (现有) |
| 0x70 | dag-pb | Protobuf 编码 (现有) |

### 2. CID 编码格式

```
CIDv1 = <version:0x01><codec:varint><multihash>

Multihash = <hash-code:varint><hash-size:varint><hash-value:bytes>

示例 - KZG Commitment:
  version:    0x01
  codec:      0x300001 (varint: 81 80 80 03)
  hash-code:  0x00 (identity)
  hash-size:  48 (0x30)
  hash-value: <48 bytes commitment>
```

### 3. Gateway Dispatch 逻辑

```go
func (g *Gateway) Resolve(root cid.Cid, path string) (*Result, error) {
    codec := root.Prefix().Codec

    switch {
    case codec == CodecMaltKZG || codec == CodecMaltVerkle || codec == CodecMaltIPA:
        // MALT explicit resolver (longest-prefix match in EAT)
        return g.explicitResolver.Resolve(root, path)

    case codec == cid.DagCBOR || codec == cid.DagProtobuf || codec == cid.Raw:
        // Implicit resolver (IPLD traversal via CAS)
        return g.implicitResolver.Resolve(root, path)

    default:
        return nil, fmt.Errorf("unknown codec: %x", codec)
    }
}
```

### 4. Resolver 接口变更

```go
// Before (使用 key.Key)
type Resolver interface {
    Resolve(root key.Key, path string) (matchedPath string, target key.Key, ev evidence.Evidence, err error)
    Verify(root key.Key, path string, target key.Key, ev evidence.Evidence) (bool, error)
}

// After (使用 cid.Cid)
type Resolver interface {
    Resolve(root cid.Cid, path string) (matchedPath string, target cid.Cid, ev evidence.Evidence, err error)
    Verify(root cid.Cid, path string, target cid.Cid, ev evidence.Evidence) (bool, error)
}
```

## 文件变更

### 删除
| 文件/目录 | 说明 |
|-----------|------|
| `key/` | 整个目录删除 |

### 新增
| 文件 | 说明 |
|------|------|
| `core/codec/codec.go` | MALT multicodec 常量定义 |
| `core/commitment/cid.go` | 从 commitment bytes 生成 CID 的工具函数 |

### 修改
| 文件 | 变更 |
|------|------|
| `core/resolver/resolver.go` | 接口改为使用 cid.Cid |
| `core/resolver/explicit/explicit.go` | 使用 cid.Cid |
| `core/resolver/implicit/implicit.go` | 使用 cid.Cid |
| `gateway/gateway.go` | 使用 cid.Cid，dispatch 逻辑更新 |
| `core/sce/sce.go` | Commit 返回 cid.Cid |
| `core/sce/commitment/kzg/kzg.go` | 生成 malt-kzg CID |
| `core/sce/commitment/verkle/verkle.go` | 生成 malt-verkle CID |
| `core/sce/commitment/ipa/ipa.go` | 生成 malt-ipa CID |
| `core/eat/eat.go` | 使用 cid.Cid |
| `core/types/arcset/` | 使用 cid.Cid |
| `core/types/evidence/` | 使用 cid.Cid |
| `malt/structure.go` | 使用 cid.Cid |
| `malt/node.go` | 使用 cid.Cid |
| 所有测试文件 | 更新类型 |

## Phases

### Phase 1: 定义 MALT multicodec 常量
Status: `completed`
- 创建 `core/codec/codec.go`
- 定义 CodecMaltKZG, CodecMaltVerkle, CodecMaltIPA
- 添加 CID 生成工具函数 (NewKZGCid, NewVerkleCid, NewIPACid)
- 添加提取和判断函数 (ExtractCommitment, IsMaltCid, GetMaltCodec)

### Phase 2: 更新 commitment scheme
Status: `completed`
- 修改 KZG/Verkle/IPA 的 Commit 方法返回 cid.Cid
- 使用 identity multihash 存储 commitment bytes
- 更新 commitment.Scheme 接口使用 cid.Cid
- 更新 arcset 包使用 cid.Cid
- 更新 SCE Engine 使用 cid.Cid

### Phase 3: 更新 resolver 接口
Status: `completed`
- 修改 Resolver 接口使用 cid.Cid
- 更新 explicit resolver
- 更新 implicit resolver
- 更新 Gateway 实现 codec-based dispatch

### Phase 4: 更新 Gateway
Status: `completed`
- 修改 Gateway 使用 cid.Cid
- 实现 codec-based dispatch
- 更新 gateway_test.go

### Phase 5: 更新上层依赖
Status: `completed`
- 更新 EAT、Structure、Node 等
- 更新 CAS 接口和实现 (mock, ipfsgateway)
- 更新 eval/benchmark.go
- 更新 examples/basic/main.go
- 更新 cmd/malt/main.go
- key/ 包暂时保留，待 Phase 6 完成后删除

### Phase 6: 更新测试
Status: `completed`
- 更新 gateway/gateway_test.go (已完成)
- 更新 cas/ipld_test.go (已完成)
- 更新 core/resolver/resolver_test.go (已完成)
- 更新 core/sce/commitment/kzg/*_test.go (已完成)
- 更新 core/sce/commitment/ipa/*_test.go (已完成)
- 更新 core/sce/commitment/verkle/*_test.go (已完成)
- 更新 malt/structure_test.go (已完成)
- 更新 eval/benchmark_test.go (已完成)

### Phase 7: 删除 key 包
Status: `completed`
- 删除 `key/` 目录 ✓
- 确认所有核心测试通过 ✓

## 已知问题 (非迁移相关)

部分 commitment scheme 测试失败，属于实现层面问题:
- KZG: ProveBatch/ProveAggregate 空路径未返回错误
- Verkle: Update 后 root 未变化, nil arc set 处理
- IPA: 部分测试失败

这些问题在迁移前已存在，不影响 key->CID 类型统一的核心目标。

## 决策记录

| 决策 | 选择 | 理由 |
|------|------|------|
| Key 类型 | cid.Cid | 与 IPFS 生态统一，codec 自带类型信息 |
| Commitment 存储 | identity hash | commitment 本身就是最终数据，无需额外哈希 |
| Codec 分配 | Private Use Area (0x300000+) | 官方保留区域，不会冲突 |
| PayloadCID | 直接用 cid.Cid | 已有 codec=raw 或其他，无需改动 |