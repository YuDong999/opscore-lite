// 单模块独立运行入口 —— 与 vite.module.config.ts 配套(MODULE=<id> 构建注入)。
// 用途: 模块单独构建/单独部署/单独开发的载体。provider 套与 App.tsx 保持一致
// (ThemeProvider/HashRouter/HostProvider/ToastProvider), 不含侧栏与 TopBar。
// 以后新增模块无需改本文件(import.meta.glob 自动覆盖)。
import React from 'react'
import ReactDOM from 'react-dom/client'
import { HashRouter } from 'react-router-dom'
import { ThemeProvider } from './theme'
import { HostProvider } from './components/HostContext'
import { ToastProvider } from './components/Toast'
import './index.css'

declare const __MODULE__: string

// loader 由构建期虚拟模块注入: 只包含本次构建目标模块的动态 import
import loader from 'virtual:standalone-module'

function Standalone() {
  const Comp = React.lazy(loader)
  return (
    <React.Suspense fallback={<div className="log-loading">加载中...</div>}>
      <Comp />
    </React.Suspense>
  )
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ThemeProvider>
      <HashRouter>
        <HostProvider>
          <ToastProvider>
            <Standalone />
          </ToastProvider>
        </HostProvider>
      </HashRouter>
    </ThemeProvider>
  </React.StrictMode>,
)
