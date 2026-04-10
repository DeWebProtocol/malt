# 1. Introduction

基于默克尔有向无环图（Merkle DAG）的内容寻址存储系统（如IPFS~\cite{benet2014ipfs}）已被证明在不可变内容分发、复制和完整性验证方面极为高效。这类系统通过将对象身份与内容绑定，提供了强大的密码学保证，从而实现可扩展的去中心化存储。然而，许多构建在去中心化存储之上的新兴应用日益需要支持**演化结构**——即随时间变化但仍保持稳定身份与可验证关系的对象逻辑组织。

在基于默克尔DAG的系统中，结构是隐式表达的。遍历关系通过哈希链接直接编码到对象内容中，最终形成的结构会作为对象身份的一部分被密码学固化。这种设计非常适合不可变数据，但它从根本上将结构演化与对象身份耦合在一起。因此，任何结构变更（例如扩展集合、重组引用或重新定义遍历语义）都必然会产生新的对象标识符，并沿祖先路径传播更新。这些影响在实践中表现为遍历延迟增加和重写开销，但更深层次反映了默克尔DAG模型中结构的表达与固化方式。

这种耦合并非特定实现产生的人为产物，而是将结构视为对象内容隐式属性的必然结果。当遍历语义和结构关系被直接编码到不可变对象中时，结构只能通过改变对象身份来实现演化。试图缓解这些代价的系统会将演化结构外化为由应用管理的辅助状态（例如可变指针或索引）。虽然这些方法在实践中有效，但由于将结构移出了内容寻址提供的密码学闭包，它们削弱了端到端的可验证性。

本文提出**可变结构层**（MALT），这是内容寻址存储之上的系统级抽象，能在保持密码学可验证性的同时将结构与数据分离。MALT并非提出新的存储基础设施或数据结构，而是通过将结构作为具有独立验证边界的显式可演化实体，对现有不可变对象存储形成补充。遍历关系被表示为显式固化的结构元素（如弧），这些元素独立于对象内容，且无需重写底层对象即可演化。

通过改变结构演化与验证的基本单元，MALT使得应用能够独立于不可变数据来表达和更新逻辑组织，同时在不信任存储和解析的情况下保持密码学完整性。验证以节点相对（root-relative）、可组合（compositional）的方式执行：遍历计算可委托给不可信的解析器执行，而客户端则根据显式固化的结构验证每个遍历步骤。该设计保持了对现有内容寻址系统的兼容性，并支持隐式与显式结构共存的混合部署。最关键的是，MALT不会改变底层内容寻址存储的对象身份、内容寻址或存储语义。

总结而言，本文作出以下贡献：

1. 我们指出了基于默克尔有向无环图（Merkle DAG）的内容寻址存储系统（CAS）存在结构性缺陷：将结构隐式嵌入对象内容会导致结构演变与对象标识强耦合，从而在保持密码学安全性的前提下，难以灵活表达和验证动态变化的结构。
2. 我们提出名为MALT的互补抽象层（complementary abstraction），通过显式且可演进的结构表示（makes structure explicit and evolvable），使得逻辑组织能够独立于不可变对象内容进行修改，同时维持密码学可验证性。
3. 我们设计了节点相对的组合式验证模型，将遍历执行与正确性验证解耦，允许不受信任的组件解析结构，并由客户端完成本地验证。
4. 实验证明，MALT可作为兼容性覆盖层实现在现有CAS系统之上，无需修改对象格式或存储语义即可支持混合遍历模式。

# 2. Background and Problem Analysis

默克尔有向无环图（Merkle DAG）构成了许多内容寻址存储系统的结构支柱，为数据完整性和不可篡改性提供了强有力的保障。然而，当将其应用场景从不可变归档扩展到网络环境中编码应用层语义时，默克尔DAG暴露出若干根本性局限。本节将从基本原理出发剖析这些局限性：我们首先论证遍历与重写操作的内在耦合会导致网络化内容寻址存储系统产生不可避免的性能损耗，进而指出默克尔DAG的表达能力边界会彻底阻碍某些语义关联的呈现。这些局限性表明，我们需要在默克尔DAG之上构建额外的抽象层。

## 2.1 Merkle DAGs and Implicit Traversal

在基于Merkle DAG的系统中，对象是不可变的，并通过其内容的加密哈希值进行标识。遍历关系通过嵌入父对象中的哈希链接隐式表示，客户端可通过递归跟随这些链接从根对象导航至其派生对象。该模型提供了强大的完整性保证：对对象的任何修改都会改变其标识符，并能够立即被检测到。然而，定义遍历语义的哈希链接同时也决定了更新如何在结构中传播。图~\ref{fig:rewrite}展示了一个典型默克尔DAG中哈希链接的这种双重作用。

![Fig.1 Traversal and rewrite in a Merkle DAG. Hash links define traversal from parent to child, while any modification propagates in the reverse direction, requiring recomputation of all ancestor hashes. Traversal and rewrite operate over the same implicit arcs but in opposite directions.](./figures/rewrite.png)

Fig.1 Traversal and rewrite in a Merkle DAG. Hash links define traversal from parent to child, while any modification propagates in the reverse direction, requiring recomputation of all ancestor hashes. Traversal and rewrite operate over the same implicit arcs but in opposite directions.

如图 1 所示，遍历操作沿着哈希链接从父节点向子节点进行，而对叶子节点的任何修改则会沿相反方向传播：从子节点到父节点，这需要重新计算祖先节点的哈希值。关键在于，遍历与重写遵循同一组隐式弧线，但方向相反。因此，任何改变遍历关系的结构演化，都必然引发遍历所依赖路径上的重写操作，从而直接将遍历性能与更新传播紧密关联。

这种方向反转具有重要影响。由于每个父节点都承诺其子节点的哈希值，更新后代对象需要重新计算从该节点到根节点路径上所有祖先的内容（即标识符）。因此，这种重写传播本质上具有祖先依赖性：只有能够访问完整祖先链的实体才能执行此操作，无论是通过本地维护还是额外通信获取。在分布式CAS系统中，当祖先节点位于远程节点或从本地缓存中清除时，这种依赖关系会直接转化为结构更新时的重写放大和额外的元数据流量。

遍历与重写的强耦合性不仅是效率问题。它从根本上限制了基于Merkle DAG系统的结构演化方式。即使底层数据保持不变，看似轻量的结构更新（如扩展集合或聚合新增引用）也会触发遍历路径上的递归重写。在下一小节中，我们将探讨这种结构特性如何在常见的演化工作负载中表现为重写放大和元数据放大。

## 2.2 Evolving Structure and Rewrite Amplification

许多构建在内容可寻址存储（CAS）上的应用不仅需要不可变数据，还需要具备**结构演化**能力：即对象的逻辑组织会随时间变化，而先前创建的对象仍保持有效且可寻址。典型场景包括扩展集合、将现有对象按新逻辑分组聚合，或在不修改底层数据的情况下重组遍历路径。

在基于默克尔有向无环图（Merkle DAG）的系统中，此类结构演化通过遍历关系来实现。然而，当遍历语义通过嵌入对象内容的哈希链接隐式表达时，结构演化便与对象身份密不可分。这导致即使是逻辑上局部的结构变更，也会引发对象重写和元数据膨胀。我们通过分析几种典型的结构演化模式来剖析这一现象。

### 2.2.1 Append and Extension

追加和扩展操作会在现有逻辑结构中引入新元素。从概念上讲，这类操作是局部的：新增一个对象，而既有元素保持不变。但在隐式遍历机制下，要表达新的遍历关系，就必须在父对象中嵌入额外的哈希链接。

由于对象标识符由内容派生，修改父对象会改变其标识符，进而迫使所有引用它的祖先对象都必须重写。因此追加操作的成本并不取决于更新规模，而是与遍历结构的高度成正比。即使被追加的对象与现有数据无关且未修改任何既有关系，这种成本放大效应依然存在。

### 2.2.2 Aggregation and Reorganization

聚合与重组改变了现有对象在逻辑上的分组或遍历方式。聚合引入了新的逻辑父级，这些父级引用了一组现有对象；而重组则在不改变对象本身的情况下，调整遍历路径或层级结构。

尽管这些操作在语义上有所不同，但在隐式遍历下会引发相同的重写行为。任何新引入或修改的遍历关系都必须编码到对象内容中，从而改变对象标识符并触发所有依赖祖先的递归重写。因此，隐式遍历将语义上不同的结构演化形式统一为单一操作模式：通过重写对象内容来反映更新后的关系。

### 2.2.3 Metadata Amplification and Ancestor Dependency

隐式遍历下的重写放大本质上并非非局域性的，但其根本上是**依赖祖先的**。执行结构更新需要访问所有内容中嵌入了受影响遍历关系的祖先节点。这种访问可以通过本地状态（如版本控制系统）提供，也可以通过从存储中检索祖先对象来实现。

在大型或分布式环境中，这种祖先依赖性会转化为显著的元数据移动和协调开销。即使应用层级的变更很小，更新操作也需要沿着整个依赖链重建并传播修改后的标识符。随着结构深度或扇出的增加，维护演化中结构的成本逐渐超过存储或传输数据本身的成本。

### 2.2.4 Semantic Coupling Under Implicit Traversal

除了重写放大之外，隐式遍历还会引发一个更微妙的问题：它迫使遍历语义、应用语义和真实性约束被编码在同一个对象内容中。当遍历关系通过哈希链接隐式提交时，任何需要验证的语义关系都不得不与有向无环图（DAG）的遍历结构保持一致。

考虑这样一个应用场景：由不同作者独立创建的对象必须能够被验证彼此关联。例如，一个对象可能代表某个主体撰写并签名的声明或贡献，同时在逻辑上引用或回应另一个由不同主体创建的对象。从遍历的角度来看，人们自然会期望从被引用的对象导航到与之关联的响应集合。然而，从真实性的角度来看，每个响应都必须通过密码学方式绑定到被引用的对象，以证明其来源。在隐式遍历下，这些需求是相互冲突的。为了使关联可验证，被引用对象的标识符必须嵌入到响应对象的内容中。这样一来，应用层级的语义被编码在与预期遍历方向相反的位置。遍历语义和真实性约束在对象内容中纠缠在一起，尽管它们源于不同的关注点。

虽然可以通过引入额外的聚合对象来显式编码遍历规则以解决此类冲突，但这样做会将遍历语义提升到更高层次的结构节点中。这种方法增加了结构的复杂性，引入了额外的元数据，并且在关系或语义演变时扩大了重写传播的范围。因此，冲突并未消除，而是转移到了辅助结构中，其维护成本随着语义丰富度和结构规模的增加而增长。

这种现象反映了隐式遍历的一个更广泛的局限性：当遍历关系是提交结构的唯一机制时，语义上不同的关系被迫共享相同的表示通道。因此，不断演变的应用语义和可验证关联会系统地引发结构重写或辅助间接引用。

### 2.2.5 Implications

综上所述，这些例子表明，重写放大效应以及遍历与应用层语义之间的纠缠（ **Entanglement** ）并非特定 workload 或 implementation 的产物。这些问题就会出现，是由于遍历语义倍父对象隐式引用导致。在此模型下，结构演化与语义关系从根本上与对象身份绑定，从而在不可变的内容寻址与轻量级、语义灵活的更新之间形成了固有矛盾。

实践中，系统常将动态变化的遍历或语义关系外化为辅助状态，以避免递归重写。但这类方法以牺牲密码学可验证性为代价换取灵活性，需要依赖外部协调机制或可变元数据的可信性。

这一发现促使我们需要一种新抽象——它能让遍历关系独立于对象内容演化，同时保持密码学可验证性。下一小节将介绍MALT的设计目标，该方案通过将结构显式定义为内容寻址存储之上可演化、可验证的实体来应对这一挑战。

## 2.3 Design Goals

MALT旨在支持在不可变的内容寻址存储之上实现可验证且可演化的结构化数据。如附录A所述，Merkle DAG通过嵌入对象内容中的哈希链接直接编码结构关系。这种设计将**遍历语义（traversal semantics）**、**对象身份（object identity）**与**认证边界（authentication boundary）**紧密耦合，从而限制了结构布局，增加了证明与更新成本，并制约了结构关系的表达能力。

从系统角度看，本文区分三种本可分离却在 Merkle DAG 中被绑在一起的"深度"：应用语义上的结构深度、运行时检索所需的顺序访问深度，以及承诺后端内部的认证深度。MALT 的目标并不是让这些成本消失，而是将它们重新解耦：结构布局服务于语义与检索局部性，承诺后端服务于认证与增量更新，而具体的性能表现再由状态放置方式与 backend 选择决定。

为了解决这些限制，MALT围绕以下目标进行设计。

### **2.3.1 Low-Latency Structural Retrieval**

遍历结构应能采用浅层布局，以最小化在结构解析过程中访问的对象数量。在 Merkle DAG 中，浅层布局会导致证明规模庞大且重写成本（rewrite）高昂，因为结构关系被嵌入到对象内容中。MALT的目标是消除这一限制，使得遍历布局可以针对低延迟检索进行优化，而不会产生高昂的证明或更新成本。

### **2.3.2 Verifiable Structural Relations**

对象之间的结构关系必须能够通过密码学验证。客户端应能在**不检索或验证整个结构**的情况下，验证特定结构关系的存在。这需要能在单个结构关系（individual structural relation）层面而非对象级哈希链接层面运作的验证机制。

### 2.3.3 Localized Structural Evolution

结构更新应当局部化。修改结构关系时，只应影响对应的关系，而不应触发对祖先对象或结构中无关部分的改写。消除这种改写放大效应（write amplification），对于在不可变对象上支持可变结构至关重要。

### 2.3.4 **Expressive Structural Relations**

系统应支持灵活的结构关系，不受哈希链接编码限制的约束。在默克尔有向无环图中，对象引用必须在构建时嵌入父对象中，这限制了可表示结构的范围。MALT通过显式表示结构关系消除了这一限制，使应用程序能够定义自然反映其语义的结构图。

## 2.4 Why Existing Workarounds Are Not Enough

已有系统通常通过若干旁路机制来缓解 Merkle DAG 在结构演化上的代价，例如可变根指针、外部索引或关系日志、以及通用的认证映射结构。这些方法在实践中各有价值，但它们并不能同时满足 MALT 所追求的三个目标：结构关系可验证、局部结构更新、以及与底层不可变 payload 的清晰分离。

第一类方法是 **mutable root indirection**，例如通过一个可变名称或指针将"当前根"绑定到某个最新对象。这类方法能够认证入口的变化，但它认证的是"当前根指向哪里"，而不是结构内部的关系本身。换言之，它可以解决 root mutability，却不能为内部显式关系提供细粒度证明，也不能将局部结构变化表达为独立于 payload 的可验证更新。

第二类方法是 **secondary index** 或 **relation log**。它们能够灵活地记录动态关系，并避免递归重写底层对象，因此在工程上很常见。然而，这类结构通常位于底层内容寻址对象之外，或依赖应用自定义的日志与索引语义。结果是，结构关系虽然可管理，却不再天然处于与对象内容相同的密码学闭包之内；系统仍需额外信任索引维护、日志顺序或外部协调机制。

第三类方法是 **authenticated map**，例如 HAMT 这类通用认证映射。它们是最强的近邻方案，因为显式关系确实可以被扁平化编码为 key-value 项并加以认证。但即使采用这种扁平化表示，认证和更新的成本仍然由通用映射结构主导：proof size、proof generation 与 update 开销会随着映射规模增长，而更新的局部性也只是在 generic map 意义下成立，而不是围绕 object-scoped structure root 成立。MALT 的目标并不是否认认证映射的有效性，而是提出一种面向结构关系本身的抽象，使结构认证、payload 分离与局部演化在同一系统模型内同时成立。

因此，MALT 并不是对这些 workarounds 的简单重命名，也不是把关系"外置"后再附加一个证明层。它所引入的是一个新的结构边界：对象的 payload 继续保持不可变和内容寻址，而结构关系则作为显式、可验证、可局部演化的实体被独立建模和维护。

# 3. System Abstraction

MALT 建立在一个基本但关键的抽象转变之上：将对象之间的关系从隐式表示转为显式表示。

在传统的 Merkle DAG 中，对象之间的结构关系被隐式编码在对象内容的哈希中，遍历语义由哈希链接决定。这种设计将结构表达、认证边界与对象身份紧密耦合，从而限制了结构的表达能力与更新效率。

MALT 则将结构关系显式化：对象之间的关系通过带路径标签的显式弧（explicit arc）表示，并通过密码学承诺进行认证。这样，结构可以独立于对象 payload 被定义、验证与演化，而不再依赖底层内容哈希结构。

本节仅形式化 MALT 的核心抽象：显式弧、结构承诺、逻辑对象形式以及由此得到的不变量。运行时如何解析、更新、验证，以及如何与传统 Merkle 遍历兼容，留待下一节的系统设计来说明。

| 符号 | 含义 |
| --- | --- |
| $v$ | 逻辑对象（object） |
| $p$ | 路径片段或剩余路径（path） |
| $c$ | 目标标识（target identifier） |
| $\mathcal{A}_v$ | 对象 $v$ 的显式弧集合 |
| $C_v$ | 对象 $v$ 的结构承诺 / structure root |
| $\pi$ | 认证证据（authentication evidence） |

## 3.1 Explicit Arcs

MALT 提出 **显式弧（explicit arc）** 表示结构关系：

$$
(v, p, c)
$$

其中，$v$ 为源对象，$p$ 为可在一次解析步骤中消费的路径片段，$c$ 为目标标识。该弧表示对象 $v$ 经由路径片段 $p$ 指向某个后继标识：

$$
v \xrightarrow{p} c
$$

对任意对象 $v$，其全部出弧组成集合：

$$
\mathcal{A}_v = \{(p, c)\}
$$

这里的 $p$ 可以是单个 label，也可以是由多个连续 label 组成的复合路径。普通布局下，弧通常只消费一个 label；扁平布局下，同一对象可直接提交更长的复合路径，例如同时包含 $(p_1, b)$、$(p_1/p_2, c)$ 与 $(p_1/p_2/p_3, d)$。我们仅要求在同一对象的弧集合中，**完整路径片段** 是唯一的，即不存在两个完全相同的 $p$ 映射到不同目标；至于某条路径是否是另一条路径的前缀，则由布局与解析策略决定。

## 3.2 Structure Commitments

为了认证对象结构，MALT 对其弧集施加结构承诺：

$$
C_v = \mathrm{Commit}(\mathcal{A}_v)
$$

该承诺绑定对象 $v$ 的全部结构关系，使得单条弧的存在性可以在不访问完整弧集合的情况下被验证。语义上，$C_v$ 是对象 $v$ 的 structure root。

### 3.2.1 Commitment Interface

任意有效的结构承诺后端都应支持与下列操作等价的能力：

- Commit: $C_v = \mathrm{Commit}(\mathcal{A}_v)$

- Prove: $\pi = \mathrm{Prove}(C_v, (p, c))$

- Verify: $\mathrm{Verify}(C_v, (p, c), \pi) \rightarrow \{true, false\}$

并满足基本正确性：

$$
(p,c)\in \mathcal{A}_v \Rightarrow \mathrm{Verify}(C_v,(p,c), \mathrm{Prove}(C_v,(p,c))) = \texttt{true}
$$

这些操作描述的是**承诺后端的要求**，而不是 MALT 对外暴露的系统接口。

### 3.2.2 Incremental Updates

MALT 将局部结构变化统一建模为"路径绑定的认证更新"。对对象 $v$ 的某个路径 $p$，设其修改前绑定为 $c$、修改后绑定为 $c'$，其中 $c$ 与 $c'$ 都表示该路径关联的目标标识，而 $\bot$ 表示该路径当前为空，则新的结构承诺定义为：

$$
C_v' = \mathrm{Update}(C_v, p, c, c')
$$

这一统一原语涵盖三类常见变化：插入对应 $\bot \rightarrow c$，删除对应 $c \rightarrow \bot$，替换对应 $c \rightarrow c'$。抽象层在这里要表达的是**作用域**而不是运行时流程：一次更新首先作用于对象 $v$ 的显式结构及其承诺状态，而不要求重写无关 payload。若该对象的 structure root 又被更高层 MALT 结构组合和引用，则上层结构根仍可能随之推进；但这种传播表现为对上层已提交 root 引用及其承诺状态的更新，而不是 Merkle DAG 式对祖先不可变对象内容的递归重写。具体如何局部维护承诺状态，取决于后端实现；后续设计中的写路径、EAT 维护和 lineage 管理，都是对这一统一更新规则的实现。

## 3.3 Logical Objects

在 MALT 中，我们定义对象为：

$$
Object_v = (payload_v, \mathcal{A}_v)
$$

其中 $payload_v$ 为对象内容，$\mathcal{A}_v$ 为对象的显式结构关系集合。这个定义是逻辑上的：它说明对象同时具有内容与结构，但不要求二者在物理实现上由同一个哈希承诺共同编码。

对一个 MALT 原生对象而言，其结构入口由 $C_v$ 给出。若对象同时携带 payload，则系统通过保留路径标签（例如 `@payload`）将结构入口绑定到该 payload 的 CID；若对象不携带 payload，则该对象可以仅由结构承诺表示。换言之，MALT 原生遍历的起点是 structure root，而 payload 仍保持为独立的、不可变的内容寻址块。

在物理实现上，$payload_v$ 的编码方式与普通 IPLD 数据块完全一致：它先由既有 multicodec 编码，再计算得到 CID，并由底层 CAS 存储。直接访问该 payload CID，只会得到对应的数据块本身，而不会隐式返回其结构关系。相对地，structure root 是一个由 MALT 解释的逻辑对象入口：若客户端直接解引用某个 bare structure root，则系统会先解析其保留弧 `@payload` 得到 payload CID，再从 CAS 中取得该 payload；若 $payload_v=\bot$，则该 bare root 物化为空内容（例如 `content-length: 0`）。若客户端访问的是 `root/path`，则后续解析按 prefix consumption 规则继续进行。除少量系统保留标签（如 `@payload`）外，路径标签的具体语义由应用决定，而不是由底层哈希结构隐式决定。

![图2：MALT抽象。一个 MALT 对象由不可变 payload 与显式 arc 集合组成，其中 structure root 对 arc set 进行认证，而保留弧 `@payload` 将结构绑定到 payload CID。更新单条显式弧会产生新的 structure root，但不会重写 payload 或无关的弧。](./figures/malt_abstraction.svg)

图2：MALT抽象。一个 MALT 对象由不可变 payload 与显式 arc 集合组成，其中 structure root 对 arc set 进行认证，而保留弧 `@payload` 将结构绑定到 payload CID。更新单条显式弧会产生新的 structure root，但不会重写 payload 或无关的弧。

图2总结了本节引入的核心抽象。MALT 将对象的 payload 与结构关系分离：结构由显式弧集合表示，并由 structure root 认证；payload 则继续保持为独立的内容寻址块。正是这种分离，使得局部结构变化首先表现为对受影响显式弧及其承诺状态的更新；若上层结构通过引用组合这些 roots，则传播发生在更高层 root 引用上，而不是对无关 payload 或祖先对象内容的递归重写。

到此为止，第三章只定义 MALT 的核心抽象：显式弧、结构承诺、增量更新规则与逻辑对象形式。部署方式、承诺后端选择、运行时解析与更新流程，以及与传统 Merkle DAG 隐式遍历的兼容机制，都留待后续系统设计与实现部分说明。与传统 Merkle DAG 类似，MALT 只假设客户端从一个已知或外部获得的 structure root 开始；本文不将 root distribution 或并发同步作为核心贡献。

# 4. System Design

## 4.1 Architecture Overview, Boundary, and Trust Model

![图3：MALT架构。MALT作为覆盖层运行在内容寻址存储系统之上。上层的 Hybrid Resolver（EAR/Gateway）维护 hybrid dispatch 与 transcript；下层的 typed step executors 执行单步 prefix consumption。其中 explicit step executor 访问 EAT 与 SCE 生成结构证明，implicit step executor 读取 CAS 对象并按 codec 完成一步兼容遍历。](./figures/system%20architecture.png)

图3：MALT架构。MALT作为覆盖层运行在内容寻址存储系统之上。上层的 Hybrid Resolver（EAR/Gateway）维护 hybrid dispatch 与 transcript；下层的 typed step executors 执行单步 prefix consumption。其中 explicit step executor 访问 EAT 与结构承诺后端生成结构证明，implicit step executor 读取 CAS 对象并按 codec 完成一步兼容遍历。

MALT 被设计为一个构建在不可变 CAS 之上的覆盖层。底层 CAS 继续负责以 CID 标识和存储不可变对象，而 MALT 在其之上提供一个独立的结构层，用于表示、认证与演化对象之间的结构关系。换言之，MALT 并不改变底层对象格式或内容寻址语义；它改变的是"结构如何被提交和解析"的机制。

系统边界与信任边界在这里一并给出。MALT 的正确性来自结构承诺与客户端本地验证，而不来自解析器或索引本身的可信性。客户端可以将解析计算委托给 MALT 组件执行，但会依据当前结构根或当前块内容验证返回结果。因此，解析器和索引都应被视为**不可信但可验证的执行组件**：它们影响性能与可用性，但不能在不被检测到的情况下伪造正确结果。

在主原型中，EAT 与 SCE 采用**按 graph 共置（colocated）**的部署方式。原因并不只是工程简化，而是 proof generation 需要高效访问某个 graph / structure root 下的**materialized proving state**；若将多层 arc 结构分散到 DHT，而仍由单个中心化 SCE 在查询时远程聚合这些状态后再生成证明，则主路径上的解析延迟与带宽占用都会迅速放大，难以体现 MALT 将"对象层级驱动的顺序 fetch"转化为"按 graph 组织的结构查询"的优势。因此，本文将 `EAT+SCE` 共置视为主性能路径；而复制化、分片化或按 graph 的去中心化部署，则作为 durability、availability 与部署可行性的扩展讨论。

换言之，这里的关键问题不是抽象地争论"中心化还是去中心化"，而是**证明状态如何放置（state placement）**：哪些状态必须位于查询热路径上，哪些状态只需用于复制、恢复与冷启动。对 MALT 而言，EAT 记录与 SCE 所依赖的 proving state 构成了主查询路径上的热状态；而快照、lineage 元数据以及复制到 DHT 的副本，则更适合作为冷状态。

## 4.2 Operational Semantics

在运行时，我们将遍历状态写作 $(k, p)$，其中 $k$ 是当前标识，$p$ 是尚未消费的剩余路径。这里的 **key** 是设计层术语：它统一表示一次解析步骤当前所处的入口，可以是某个 MALT 原生对象的 structure root，也可以是某个传统内容块或 link-embedded object 的 CID。这里讨论的是**单步转移语义**；真正维护多步循环与 dispatch 的上层 hybrid resolution，将在后续过程小节中给出。

一次成功的解析步骤产生新的状态 $(k', p')$ 以及对应证据 $e$，记为：

$$
(k, p) \rightsquigarrow (k', p', e)
$$

并要求存在一个非空前缀 $p_1$ 满足 $p = p_1 \circ p'$。运行时有两类基本转移规则：

- **Explicit step.** 若当前 $k$ 是 structure root，且存在一条由其认证的显式弧 $(p_1, k')$，则系统执行一次显式转移，并返回该弧的结构证明。
- **Implicit compatibility step.** 若当前 $k$ 是传统 CID，且底层对象的原生遍历语义能够消费前缀 $p_1$ 到达下一跳 $k'$，则系统执行一次兼容转移，并返回当前对象内容作为该步证据。默认情况下，该步同样采用 **matched longest prefix** 语义：客户端通过校验该块的 CID，并在本地按对象 codec 重新提取 children、选择当前可继续下降的最长匹配前缀以及对应 child，验证该步解析是否正确。若默认最长匹配无法继续完成解析，则系统可以降级到另一种 resolution implementation，对同一对象执行更完整的树搜索并尝试其他较短前缀，但这属于实现层的替代策略，而不是推荐的默认行为。

上述 prefix consumption 同时覆盖普通布局与扁平布局：在普通布局中，一次转移通常只消费一个 label；在扁平布局中，同一对象已提交的复合路径可被当作一个更长的 $p_1$ 直接消费，此时默认策略就是选择可继续下降的 matched longest prefix。只要剩余路径仍存在，解析就会继续；它既可以停留在单个 graph 内完成，也可以在某一步跨到另一个 graph 的 root 后继续执行同一套规则。

实现上，解析器会为整个检索过程维护一个 transcript 数组，按步记录显式证明或隐式对象内容。显式步与隐式步的证据形式可以不同，但它们都必须足以让客户端在本地重放并验证该步结果。对 bare structure root 的直接访问可被视作先执行一次保留弧 `@payload` 的显式解析，再继续对得到的 payload CID 进行对象访问；若对象没有 payload，则该访问返回空内容。

当 $p$ 为空时，解析终止并返回当前 $k$；若两类转移均不适用，则解析失败。无论采用哪种转移，客户端都必须能够依据当前结构根或当前对象状态本地验证该步结果。

## 4.3 Component Roles

MALT 的读路径实际上分为三层：上层是执行 hybrid resolution 的入口，中层是按 key 类型完成单步 prefix consumption 的 typed step executors，下层则是为显式解析提供 proving/index state 的结构状态组件。

第一类是**Hybrid Resolver（EAR）**。EAR 负责维护完整的 `(k, p)` 解析循环、根据当前 key 类型执行 dispatch、收集 transcript，并协调下层单步执行组件。它是读侧入口，但不承担最终正确性判断。在当前原型中，这一角色由 gateway 实现。

第二类是**单步执行组件（typed step executors）**。它们实现代码层的单步 `Resolve(root, path)` 接口，并负责在一次解析步骤内消费可匹配的路径前缀，而不是负责完整的多步遍历：

- **explicit step executor**：针对 structure root 执行显式弧解析，通过 longest-prefix 方式在结构状态中找到候选目标，并生成对应的结构证明；
- **implicit step executor**：针对传统 CID 解析底层对象内容，按对象 codec 执行一次隐式遍历；对于 UnixFS、HAMT、普通 IPLD 节点等具体结构，其区别都属于这一层的内部实现细节。

第三类是**索引与证明状态（EAT + SCE）**。其中 EAT 更准确地说是一个 graph-scoped 的 **proving index**：它负责将结构根及路径前缀映射到候选目标 key，从而避免在解析时扫描完整弧集合，并为后续 proof generation 快速定位相应记录。SCE 则负责在对应结构状态上生成和验证证明。二者共同构成显式解析路径上的 proving state。EAT 是性能状态，而不是信任根。

在语义层面，可以将显式解析所依赖的索引能力理解为一个 lookup 抽象：

$$
\mathrm{Lookup}(g, r, p) \rightarrow (p_1, k') \;|\; \bot
$$

其中 $g$ 表示当前 graph，$r$ 表示当前 structure root，输出为本步匹配到的路径前缀 $p_1$ 及其目标 key $k'$。剩余路径如何更新由上层 hybrid loop 负责，而不是由 EAT 自身返回。在本文默认原型中，语义上的 `Lookup` 采用 **matched longest prefix**。对于非扁平布局，系统不推荐在同一对象下同时存在"某条路径是另一条路径前缀"的歧义组织；对于扁平布局，最长匹配通常就是期望的目标。需要注意的是，这里的 `Lookup` 是设计层的语义抽象；当前实现中的 EAT 可以只暴露更底层的 point lookup / snapshot 能力，而由 explicit step executor 在其之上实现语义上的 longest-prefix 查找。

第四类是**结构承诺后端**。该后端在设计上分化为三个逻辑角色：

- `Committer/Updater`：负责结构承诺的生成与增量更新；
- `Prover`：负责为解析结果生成认证证据；
- `Verifier`：位于客户端，负责本地验证。

此外，写侧还需要一个结构更新入口，用于接收显式弧修改请求并驱动承诺与索引状态演化。它不必在实现中表现为独立进程，但在设计上必须与读侧职责分开。

## 4.4 Resolution Procedure

上一节的组件关系可以概括为两层控制流：上层是 `Hybrid Resolver / Gateway` 维护的 hybrid resolution 循环，下层是由 explicit step executor 与 implicit step executor 分别实现的单步 prefix consumption。给定某个当前 key 和剩余路径，运行时控制流如下：

```text
HybridResolve(k, p):
  transcript <- []
  while p is not empty:
    r <- DispatchByKeyType(k)
    (p1, k_next, ev) <- r.Resolve(k, p)
    if no match:
      return bot
    emit (k, p1, k_next, ev) into transcript
    k <- k_next
    p <- ConsumePrefix(p, p1)

  if IsStructureRoot(k):
    try (p1, k_payload, ev) <- ExplicitStep.Resolve(k, "@payload")
    if matched:
      emit (k, p1, k_payload, ev) into transcript
      return k_payload
    else:
      return EmptyContentOrStructureRoot(k)

  return k
```

`DispatchByKeyType` 是这里真正的上层 hybrid 行为：若当前 key 是 structure root，则 EAR 将该步委托给 explicit step executor；若当前 key 是传统 payload CID，则 EAR 将该步委托给 implicit step executor。也就是说，真正执行 prefix consumption 的并不是上层 Hybrid Resolver 本身，而是 dispatch 之后的具体单步执行组件。

因此，`Resolution Procedure` 的职责不是重新定义解析语义，而是把它拆成两层明确的工程角色：上层 Hybrid Resolver 负责按 key 类型分派、维护多步循环、处理 bare structure root 的默认 `@payload` 物化并记录 transcript；下层 explicit / implicit step executor 负责各自的一步 prefix consumption。单步执行组件的返回值是 `matchedPath + target + evidence` 这一单步结果，而不是完整查询的最终答案。客户端随后对 transcript 中的每一步执行本地验证。

## 4.5 Update and Versioning Procedure

写侧过程实现第三章定义的统一更新原语。给定对象 $v$ 在路径 $p$ 上的修改前后绑定 $c$ 与 $c'$，系统执行如下步骤：

```text
UpdateArc(C_v, p, c'):
  c <- EAT.LookupCurrent(C_v, p)   // returns current binding or ⊥
  C_v' <- Committer.Update(C_v, p, c, c')
  EAT.Apply(C_v', p, c, c')
  RecordLineage(C_v', C_v)
  return C_v'
```

其中，写侧入口会先通过 EAT 或其他当前结构状态查询路径 $p$ 的当前绑定 $c$，再将其作为参数传给 `Committer.Update`。`EAT.Apply` 表示对索引状态执行与该绑定变化一致的维护：它可以表现为插入、删除或覆盖，但这些都是实现层对同一抽象更新原语的具体化。若底层 payload 未变，则已有数据块 CID 保持不变。这一过程与 Merkle DAG 的祖先重写不同：变更首先局部化在当前结构根的承诺与辅助索引中；若该结构根又被上层 MALT 结构作为引用目标组合，则上层结构根也可能继续更新，但更新对象是其已提交的 root 引用和对应承诺状态，而不是祖先 payload 或完整对象内容。

由于更新会产生新的结构根，EAT 条目通常按其写入时所属的结构根进行索引。当查询命中当前结构根失败时，系统可能需要沿 structure-root lineage 向前查找旧状态。这里，EAT 与 versioned lookup table 的职责只是**快速定位对应 record**；真正使用旧 commitment 还是当前 commitment 来生成证明，取决于具体的结构承诺方案，并由 SCE 决定和验证。因此，版本演化引入的是**版本相关查找**，而不是底层对象的递归重写。

## 4.6 Layout Considerations

MALT 不强制规定对象的布局方式。应用可以根据自身语义将相关对象组织在单一结构根下，也可以让一个 graph 通过引用组合其他 graph。与 Merkle DAG 不同的是，布局不再必须服务于"控制父对象扇出以避免重写传播"这一密码学约束，而可以更多服务于应用语义与解析局部性。

因此，在 MALT 中，更扁平的布局首先应被理解为**单个 graph 内部**的 layout 策略：当一组对象被组织在同一结构根下时，更新单条弧只需要修改相应的承诺状态和索引，而不需要在该 graph 内跨多层 structure root 传播变化。这也是本文在单 graph 场景下推荐 flat layout、而不推荐 per-layer authentication 的主要原因。

与此同时，graph 之间仍然可以发生组合式引用；但这与 flat layout 并不矛盾。组合式关系描述的是"一个 graph 如何引用另一个 graph 的 root"，而 flat 描述的是"单个 graph 内部如何组织自己的显式结构"。这里并不需要额外定义一套"组合式解析"机制：只要剩余路径仍非空，系统就持续根据当前 key 的类型执行统一的 explicit / implicit dispatch。若某一步得到的下一跳属于另一个 graph，则解析继续在该 graph 上执行；若所需路径已在当前 graph 内被一次性消费完毕，则这就是 single-graph resolve。此时传播会沿 graph 之间的 root 引用关系发生，但每一层只需更新其提交的 root 引用及相关承诺状态，而不会退化为 Merkle DAG 式的祖先对象内容重写。

空 payload 而仅由结构承诺表示的 structure-only node，也更适合作为一种设计模式，而不是模型层必须假定的唯一对象形态。类似地，诸如 `@payload` 这样的保留标签，应被视为实现 MALT 原生对象绑定语义的系统约定，而不应扩展为对所有应用标签的全局解释。

# 5. Implementation

前一节描述的是 MALT 必须如何工作；本节关注的是**本文原型具体实现了什么**。实现层面，我们固定若干代表性实例，以分别评估不同部署、索引组织和承诺后端下的开销与收益。

## 5.1 Prototype Overview

本文的原型将 MALT 实现为一个兼容现有 CAS 的结构覆盖层。主原型固定采用 **gateway + graph-scoped colocated EAT+SCE + 可切换承诺后端** 的组合：gateway 提供统一解析入口，而 EAT 与 SCE 在同一 proving/index 服务中共置，以最直接地暴露结构查询、证明生成与索引维护的真实开销。更具体地说，当前实现中的读路径由 `gateway` 负责 hybrid dispatch、bare root 的 `@payload` 物化与 transcript 维护，并在内部调度 explicit step executor 与 implicit step executor 两类单步解析组件。其他实现点仍然保留，但其角色是补充说明部署可行性、可移植性和 durability tradeoff，而不是替代这条主性能路径。在当前实现中，一个 graph 对应一个 bucket，因此 graph-scoped 状态在代码中通过 bucket namespace 落地。

## 5.2 Key Encoding and Dispatch

实现中需要将设计层的 key 映射为可传输和可判别的编码。对普通数据块，原有的 IPLD 编码和 CID 保持不变：$payload_v$ 先由既有 multicodec 编码，再计算得到 CID，并由底层 CAS 原样存储。对显式结构，structure root 也被编码为 MALT 可识别的 CID，但使用专门的 multicodec/codec 类型与普通 payload CID 区分。传统 IPFS 节点如果不理解该 codec，只会将其视为不透明标识；MALT 解析器则据此在运行时执行分派：识别为结构根的 key 进入显式弧路径，识别为 payload CID 的 key 进入底层对象路径。具体采用哪些 multicodec 码以及如何注册这些码，属于实现细节。

## 5.3 Commitment Backends

结构承诺后端在原型中被实例化为具体可运行的 backend。当前设计支持多种承诺方案，并通过统一的后端接口对上层提供一致的 `Commit / Update / Prove / Verify` 语义。不同后端会影响结构根的编码方式、证明大小、更新代价以及证明生成与验证开销。

提供多种 commitment backend 的目的，不是单纯展示"后端可插拔"，而是让客户端能够根据**数据规模、结构布局以及证明目标**选择更合适的承诺方案。换言之，backend 选择本身就是 MALT 系统设计的一部分：不同的 backend 与不同的数据 layout 结合时，会形成不同的解析与证明 tradeoff。

具体而言：

- 对于数据量很大、需要以树状结构组织并进行结构承诺的场景，更适合采用 **Verkle tree** 一类树状承诺；
- 对于 compositional 的按层组织、且需要在稀疏位置上进行承诺与证明的场景，更适合采用 **sparse commitment**，例如 KZG；
- 对于 append-only file 的 index commitment 这类线性或向量式结构，更适合采用 **IPA**。

因此，MALT 的后端选择不应被理解为"在同一 workload 上任意替换 backend"，而应理解为：客户端可以依据自身 workload 和 layout 选择最匹配的承诺方案，从而在 proof size、prove/update 开销与 lookup efficiency 之间取得更合适的平衡。这里也需要明确区分三种"深度"：结构语义深度决定对象关系如何组织，运行时检索深度决定查询要顺序消费多少步，而 backend 内部的 commitment depth 则主要影响 prove / update / verify 的代价。后续实验中的 backend 对比，目的正是展示这种"layout-sensitive"的 tradeoff，而不是宣称某一种 backend 对所有场景都最优。

## 5.4 EAT Realizations

EAT 在原型中有多种实现方式。主原型中，EAT 与 SCE 在同一 graph-scoped 服务中共置：EAT 负责以 `(graph, root, path)` 为语义接口定位候选记录，SCE 则在本地持有并维护对应的 materialized proving state 后生成证明。这一共置方式定义了系统的**热路径状态（hot-path state）**：查询在主路径上直接访问 EAT 与 proving state，而不依赖从远程存储按需重建完整 arc tree。若仅将 EAT 或多层 arc 状态分散存入 DHT、而仍由单个中心化 SCE 收集这些状态后再生成证明，则系统主路径仍需承担远程聚合和大规模带宽开销，难以作为 lookup latency 优势的代表实现。在当前实现中，一个 graph 对应一个 bucket，因此这里的 graph-scoped 状态在代码中落实为 bucket-scoped 状态。

需要强调的是，设计层将 EAT 视为语义上的 `Lookup(graph, root, path) -> (matchedPath, target)` 抽象；而在当前代码中，EAT 也可以只提供更底层的 point lookup、snapshot 与版本化状态访问能力，由 explicit step executor 在其之上执行 longest-prefix 搜索并触发 proof generation。换言之，语义上的 `Lookup` 与代码层的具体接口不必完全同形，但二者服务于同一条解析逻辑。

在扩展实现中，EAT 仍可进一步复制化、分片化或通过 DHT 组织，以验证 MALT 并不在语义上依赖单点索引，并改善 durability 与 availability。这里更自然的方向是复制或分片按 graph 的 `EAT+SCE` 整体单元；即使将 EAT 记录、快照或 lineage 元数据异步复制到 DHT，这些副本也主要服务于**冷状态（cold-path state）**，用于备份、恢复与可用性，而不是主查询路径上的即时 proof generation。必要时，EAT 可以从结构状态中重建。

默认原型中的 `Lookup` 采用 matched longest prefix 语义；与之对应，legacy Merkle/IPLD 对象的兼容遍历也默认按最长匹配继续下降。若默认最长匹配失败，原型可以切换到更保守的 full-tree search resolution 变体，对同一对象尝试其他候选前缀。更换底层索引结构或 fallback 策略不会改变 MALT 的抽象，只会改变解析时的具体实现与性能特征。

## 5.5 Deployment Modes

原型至少支持两种部署模式。

- **Gateway prototype**：位于 CAS 之前，提供统一的结构解析入口，适合在受控环境中测量纯净的端到端解析延迟。
- **Sidecar prototype**：与现有节点共置，展示 MALT 如何作为兼容覆盖层与去中心化 CAS 共存，并与传统 Merkle DAG 遍历形成互补。

这两种部署并非两个不同系统，而是同一 MALT 抽象在不同环境中的具体落点。它们回答的问题不同：前者更适合测量机制本身的上界与干净 tradeoff，后者更适合验证兼容性与部署可行性。无论采用哪种部署，本文都将 `EAT+SCE` 共置视为默认 proving/index 单元；若需要更强的 durability 或 availability，则更自然的扩展方向是复制或分片这一按 graph 的单元，而在当前实现中这一单元对应一个 bucket，而不是仅将 EAT 单独分散而仍由中心化 SCE 远程聚合状态。

## 5.6 Engineering Optimizations

版本化结构会引入 lineage lookup 和索引维护开销，因此原型进一步采用若干工程优化来控制这些成本。例如：

- **Copy-on-Write**：在较新结构根下复制未修改条目，以缩短后续查找路径；
- **skiplist / shortcut**：为长 lineage 提供跳跃式访问，降低最坏情况查找延迟；
- **缓存、批处理或索引压缩**：用于进一步降低解析和更新路径上的常数项开销。

这些优化不会改变 MALT 的语义，只是在保持结构认证边界不变的前提下改善系统的具体性能表现。

# 6. Evaluation

Evaluation 的目标不是堆叠配置，而是验证 MALT 是否真正改变了可验证结构演化的成本模型。为此，实验需要围绕少数几个明确问题组织：MALT 是否将 Merkle DAG 的祖先传播成本转化为局部结构维护成本；这种成本是否只是被转移到了 EAT；不同 backend 与 layout 的组合如何改变 prove / update / lookup tradeoff；以及这些收益在兼容部署下依赖哪些热路径状态。

## 6.1 Experimental Setup

实验设置需要统一给出 workload、baseline、metrics 与部署配置。baseline 至少包括：

- **UnixFS**：作为隐式 Merkle DAG 遍历的主 baseline；
- **HAMT**：作为最强的 authenticated-map baseline；
- **MALT-flat**：作为本文主原型与主张对应的 MALT 配置。

除系统 baseline 外，实验还应区分两类场景因素：一类是**单 graph 内部 layout**，重点比较 flat layout 与 per-layer authentication；另一类是**解析范围**，即 single-graph resolve 与 multi-graph resolve。核心指标应覆盖 rewrite amplification、metadata amplification、proof size、prove / verify / update cost、end-to-end resolution latency、lineage depth 与 EAT state growth。实验讨论中需要显式区分三种深度：结构语义深度、运行时检索深度与 commitment backend 内部深度。

## 6.2 Main Results

主结果节回答最核心的问题：与 UnixFS 和 HAMT 相比，MALT 是否真正改变了主要成本模型。这里应重点报告 rewrite amplification、metadata amplification、retrieval latency 与 proof size，并将 `MALT-flat` 作为主角来呈现。若需要展示 graph 之间的组合式关系，应将其组织为 single-graph 与 multi-graph resolve 的场景差异，而不是引入一个与 `MALT-flat` 并列的"第二个 MALT 系统"。

## 6.3 Cost Breakdown

这一节专门回应"你是不是只是把成本转移到了 EAT 上"的质疑。实验应将总成本进一步分解为 object rewrite cost、index maintenance cost、proof generation cost、verification cost 与 payload fetch cost，并结合 lineage lookup、full-COW worst case 与 EAT state growth 说明这些成本在 MALT 中是如何被局部化和控制的。

## 6.4 Backend and Layout Tradeoffs

这一节讨论 backend、单 graph 内部 layout 与解析范围如何共同影响系统行为。重点不是比较某个 backend 是否在所有场景下都更快，而是说明：

- 在单 graph 内，flat layout 与 per-layer authentication 在 retrieval latency、传播范围与 lookup cost 上的差异；
- 在 graph 之间，single-graph resolve 与 multi-graph resolve 如何共享同一套 prefix-consumption 语义；
- Verkle、KZG 与 IPA 分别在何种 workload / layout 下更合适；
- backend 内部的 commitment depth 不应与应用可见的 retrieval depth 混为一谈。

## 6.5 Deployment and Compatibility

这一节评估部署可行性，而不是宣称任意部署都能获得同样的 latency 优势。`gateway` 模式用于测量受控环境中的纯净端到端解析延迟，`sidecar` 模式用于展示与现有去中心化 CAS 的兼容性。若引入 DHT 或复制化状态，其角色应被解释为 durability / availability / recovery 的扩展路径；主性能路径仍以按 graph 共置的 `EAT+SCE` proving/index 单元为准，而在当前实现中这一单元对应一个 bucket。

# Appendix

## A. Merkle DAGs的结构性性能限制

本附录的目标不是泛泛证明"所有认证结构都有深度代价"，而是刻画一种更具体的 commitment model：**embedded-reference Merkle commitment**。在该模型中，对象的可验证外部关系被直接嵌入父对象内容中，因而 traversal semantics、object identity 与 authentication boundary 被绑定在同一对象表示里。附录A要说明的是：正是这种绑定，而不是某个具体实现细节，导致了祖先传播式更新、对象深度驱动的认证成本，以及语义表达能力的边界。

### A.1 Embedded-Reference Commitment Model

我们首先限定分析对象：一个对象 $v$ 被序列化为其 payload 与一组嵌入式外部引用的组合，而对象身份由该序列化结果的哈希决定。于是，若对象的已认证 outgoing relation 发生变化，则对象自身的标识必然变化。也就是说，在 embedded-reference Merkle commitment 中，被认证的结构关系并不是附着在对象身份之外的独立状态，而是对象身份本身的一部分。

这一建模范围十分重要：附录A所证明的是 **embedded committed edges** 的结构性后果，而不是所有 authenticated data structures 的共同命运。其他承诺结构也可能有自己的深度成本，但那已经是不同的 cost model。

### A.2 Identity Propagation Under Local Structural Update

若某对象 $v$ 的某条已认证关系被修改，则 $v$ 的序列化结果变化，从而其 identity 也变化。若父对象 $u$ 通过嵌入式引用提交了 $v$ 的 identity，则 $u$ 的序列化结果同样变化；这一传播继续沿祖先方向递归发生。

因此，在该 commitment model 下，局部结构更新并不会停留在"单条关系"这一层，而会表现为对祖先对象身份的传播式更新。所谓 rewrite amplification，不只是工程布局不佳，而是 embedded-reference commitment 的直接结果。

### A.3 Fanout Bound and Committed-Depth Lower Bound

设 $S_{\max}$ 为对象最大大小，$\lambda$ 为单个引用的序列化大小，则对象可直接承载的引用数量受物理扇出约束：

$$
f_{\max} = \left\lfloor \frac{S_{\max}}{\lambda} \right\rfloor .
$$

若需表示 $N$ 个逻辑对象，而每层 committed object 的最大扇出为 $f_{\max}$，则 committed object depth 至少满足：

$$
\delta \ge \log_{f_{\max}} N.
$$

这里的 $\delta$ 指的是 **committed object depth**，而不是任意 commitment backend 的内部深度。对 embedded-reference Merkle commitment 而言，认证与检索都必须沿这条对象链顺序进行，因此 retrieval cost、proof size 与更新传播范围都会随 committed depth 增长。换言之，应用语义深度、运行时检索深度与对象认证边界在该模型下被绑在一起。

### A.4 Expressiveness Limit of Acyclic Embedded Commitment

若每一条需要被认证的语义关系都必须通过 embedded committed edge 表达，则语义图必须服从该 committed graph 的偏序结构。由于 embedded-reference Merkle commitment 的依赖图必须是有向无环的，因此任何要求互相依赖、双向绑定或更一般强连通关系的语义图，都无法直接由这种 commitment model 表达。

因此，implicit arcs 的问题不只是"树太深"。更深层的问题是：一旦认证边界被固定在嵌入式引用上，语义表达能力也被同步限制在一个 acyclic committed graph 上。

### A.5 Scope of the Result

综上，附录A证明的是 embedded-reference Merkle commitment 的三个结构性后果：

- 局部结构更新会诱导 ancestor-dependent identity propagation；
- 认证与检索成本受 committed object depth 约束；
- 语义关系必须服从 acyclic embedded commitment 的表达边界。

这一定理组并不否认其他认证结构也有自己的成本；它只说明：当 traversal semantics 被编码进父对象的嵌入式 hash link 时，上述三类限制是结构性的。MALT 的意义正是在于把结构从这种 object-identity propagation 中解耦出来。
