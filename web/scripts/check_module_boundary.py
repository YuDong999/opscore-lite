# -*- coding: utf-8 -*-
"""模块边界审计: 谁穿透了谁(L3 模块独立性机械化验证)。
判据(components/common/README.md): 把整个模块目录删掉, 其他模块不受影响 = 独立模块。
规则:
  - 模块内文件 import 其他模块的内部文件 = 跨模块穿透(L3→L3 禁止, 需提升 L2 或收敛)
  - import L1(components/ui) / L2(components/common, lib) / app 层(api/client, theme, App) = 合法
  - 循环依赖(模块 A 的文件链最终 import 回 A 的入口之外另一个模块)同上判定
用法: python check_module_boundary.py [--root <src 目录>]"""
import os, re, sys, collections

ROOT = sys.argv[sys.argv.index('--root') + 1] if '--root' in sys.argv else os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'src')
MOD_DIR = os.path.join(ROOT, 'modules')
IMP_RE = re.compile(r"""(?:import|export)\s[^;'"]*?from\s+['"]([^'"]+)['"]|import\(\s*['"]([^'"]+)['"]\s*\)""")

# 1) 枚举模块: 顶层单文件 + 目录模块(以 index.tsx 为入口)
modules = {}   # 模块名 -> 入口文件
for f in sorted(os.listdir(MOD_DIR)):
    fp = os.path.join(MOD_DIR, f)
    if f.endswith('.tsx') or f.endswith('.ts'):
        modules[f.rsplit('.', 1)[0]] = fp
    elif os.path.isdir(fp) and os.path.exists(os.path.join(fp, 'index.tsx')):
        modules[f] = os.path.join(fp, 'index.tsx')

# 2) 模块内文件清单(目录模块的全部源文件)
def module_files(name):
    fp = modules[name]
    base = os.path.dirname(fp)
    return {base} if os.path.basename(fp) == 'index.tsx' and os.path.basename(base) == name else set()

def resolve(spec, from_file):
    """把 import 说明符解析为 src 内绝对路径(相对路径), 返回 None 表示外部/别名/非文件。"""
    if not spec.startswith('.'):
        return None
    base = os.path.dirname(from_file)
    cand = os.path.normpath(os.path.join(base, spec))
    for ext in ('.tsx', '.ts', '/index.tsx', '/index.ts', ''):
        if os.path.exists(cand + ext) and os.path.isfile(cand + ext):
            return cand + ext
    return None

def owner_of(path):
    """文件属于哪个模块(仅目录模块成员返回模块名; 顶层模块文件返回自身模块名)。"""
    rel = os.path.relpath(path, MOD_DIR)
    if '\\' in rel or '/' in rel:
        return rel.replace('\\', '/').split('/')[0]
    return rel.rsplit('.', 1)[0]

def is_l2_or_l1(spec_or_path):
    p = spec_or_path.replace('\\', '/')
    return ('/components/ui/' in p or '/components/common/' in p or '/lib/' in p
            or p.startswith('components/ui') or p.startswith('components/common') or p.startswith('lib/'))

# 聚合壳豁免(2026-09-19 设计决策): 容器管理/服务发现等页面是有意的模块组合视图,
# 它们引用子模块 = 页面组合, 与"顺手引用别人内部组件"的穿透性质不同。
# 豁免仅限「组合壳引用整个子模块入口」, 引用子模块内部文件仍算穿透。
AGGREGATORS = {'ContainersModule', 'ServicesModule', 'NetworkModule'}

# 3) 逐模块收集 import(递归目录模块内文件)
violations = collections.defaultdict(list)
compositions = []                            # 聚合壳的组合关系(豁免, 仅记录)
for name, entry in modules.items():
    files = module_files(name)
    seen = set()
    queue = [entry]
    while queue:
        f = queue.pop()
        if f in seen: continue
        seen.add(f)
        try: text = open(f, encoding='utf-8').read()
        except Exception: continue
        for m in IMP_RE.finditer(text):
            spec = m.group(1) or m.group(2)
            if not spec: continue
            target = resolve(spec, f)
            if target is None: continue          # 别名(@/)或包: 别名单独统计
            if is_l2_or_l1(target): continue     # L1/L2 合法
            if target in files or owner_of(target) == name: continue  # 模块内部
            target_owner = owner_of(target)
            if target_owner in modules and target_owner != name:
                if name in AGGREGATORS:
                    compositions.append((name, target_owner, os.path.relpath(f, MOD_DIR)))
                else:
                    violations[name].append((os.path.relpath(f, MOD_DIR), spec, target_owner))
            queue.append(target)                 # 递归追传递依赖
        # 别名导入也检查(@/components/DatabaseManager/... = 跨进别的模块目录)
        for m in re.finditer(r"""from\s+['"]@/([^'"]+)['"]""", text):
            p = m.group(1)
            if p.startswith('modules/') and not p.startswith(f'modules/{name}'):
                viol_owner = p.split('/')[1]
                if viol_owner in modules:
                    violations[name].append((os.path.relpath(f, MOD_DIR), '@/' + p, viol_owner))

# 4) 报告
print(f'模块数: {len(modules)}')
if compositions:
    print('组合关系(豁免):')
    for name, target, f in compositions:
        print(f'  {name} 组合 {target}  ({f})')
ok, bad = [], []
for name in sorted(modules):
    v = violations.get(name, [])
    (bad if v else ok).append(name)
    tag = '❌' if v else '✅'
    extra = ''
    if v:
        kinds = collections.Counter(t for _, _, t in v)
        extra = '  穿透目标: ' + ', '.join(f'{k}×{c}' for k, c in kinds.most_common())
    print(f'  {tag} {name}{extra}')
print()
if bad:
    print('== 穿透明细 ==')
    for name in bad:
        for f, spec, target in violations[name][:8]:
            print(f'  {name}: {f} -> {spec}  (目标模块: {target})')
print(f'\n达标 {len(ok)} / 越界 {len(bad)}')
