import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from 'react'
import { Link, Navigate, Outlet, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { Button } from './components/ui/button'
import { Toaster } from './components/ui/toaster'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from './components/ui/tooltip'
import {
  LogOut,
  Server,
  Network,
  Route as RouteIcon,
  Share2,
  Waypoints,
  Settings as SettingsIcon,
  Loader2,
  Sun,
  Moon,
  ShieldCheck,
  Globe2,
} from 'lucide-react'
import { ApiError, AUTH_EXPIRED_EVENT, listNodes, logout } from './api/client'
import { toast } from './lib/toast'
import { LOGO_DARK_URL, LOGO_LIGHT_URL } from './lib/logos'

const Login = lazy(() => import('./pages/Login'))
const Fleet = lazy(() => import('./pages/Fleet'))
const Inbounds = lazy(() => import('./pages/Inbounds'))
const Subscriptions = lazy(() => import('./pages/Subscriptions'))
const ProxyChains = lazy(() => import('./pages/ProxyChains'))
const RoutePlans = lazy(() => import('./pages/RoutePlans'))
const Settings = lazy(() => import('./pages/Settings'))
const PKI = lazy(() => import('./pages/PKI'))
const DNSCertificates = lazy(() => import('./pages/DNSCertificates'))

const NAV = [
  { path: '/', label: '节点', icon: <Server className="h-4 w-4" /> },
  { path: '/inbounds', label: '入站', icon: <Network className="h-4 w-4" /> },
  { path: '/chains', label: '代理链', icon: <Waypoints className="h-4 w-4" /> },
  { path: '/route-plans', label: '路由计划', icon: <RouteIcon className="h-4 w-4" /> },
  { path: '/subscriptions', label: '订阅', icon: <Share2 className="h-4 w-4" /> },
  { path: '/pki', label: '证书', icon: <ShieldCheck className="h-4 w-4" /> },
  { path: '/dns-certificates', label: 'DNS/ACME', icon: <Globe2 className="h-4 w-4" /> },
  { path: '/settings', label: '设置', icon: <SettingsIcon className="h-4 w-4" /> },
] as const

function PageFallback() {
  return (
    <div className="flex items-center justify-center min-h-[300px]">
      <Loader2 className="h-6 w-6 animate-spin text-zinc-500" />
    </div>
  )
}

function AppLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const [ready, setReady] = useState(false)
  const [authed, setAuthed] = useState(false)
  const [bootError, setBootError] = useState('')
  const [loggingOut, setLoggingOut] = useState(false)
  const [theme, setTheme] = useState<'light' | 'dark'>(() => {
    return document.documentElement.classList.contains('dark') ? 'dark' : 'light'
  })

  const toggleTheme = () => {
    const next = theme === 'dark' ? 'light' : 'dark'
    setTheme(next)
    if (next === 'dark') {
      document.documentElement.classList.add('dark')
    } else {
      document.documentElement.classList.remove('dark')
    }
    localStorage.setItem('la-theme', next)
  }

  const loginPath = useCallback(() => {
    const next = `${location.pathname}${location.search}`
    return `/login?next=${encodeURIComponent(next)}`
  }, [location.pathname, location.search])

  const checkSession = useCallback(async () => {
    setReady(false)
    setBootError('')
    try {
      await listNodes()
      setAuthed(true)
    } catch (err) {
      setAuthed(false)
      if (!(err instanceof ApiError && err.status === 401)) {
        setBootError(err instanceof Error ? err.message : '无法连接控制面')
      }
    } finally {
      setReady(true)
    }
  }, [])

  useEffect(() => {
    void checkSession()
  }, [checkSession])

  useEffect(() => {
    const onExpired = () => {
      setAuthed(false)
      setBootError('')
      setReady(true)
      navigate(loginPath(), { replace: true })
    }
    window.addEventListener(AUTH_EXPIRED_EVENT, onExpired)
    return () => window.removeEventListener(AUTH_EXPIRED_EVENT, onExpired)
  }, [loginPath, navigate])

  useEffect(() => {
    if (ready && !authed && !bootError) navigate(loginPath(), { replace: true })
  }, [ready, authed, bootError, loginPath, navigate])

  const active = useMemo(() => {
    const path = location.pathname
    if (path.startsWith('/inbounds')) return '/inbounds'
    if (path.startsWith('/chains')) return '/chains'
    if (path.startsWith('/route-plans')) return '/route-plans'
    if (path.startsWith('/subscriptions')) return '/subscriptions'
    if (path.startsWith('/settings')) return '/settings'
    if (path.startsWith('/pki')) return '/pki'
    if (path.startsWith('/dns-certificates')) return '/dns-certificates'
    return '/'
  }, [location.pathname])

  useEffect(() => {
    const item = NAV.find((entry) => entry.path === active)
    document.title = item ? `${item.label} · Ladder Airport` : 'Ladder Airport'
  }, [active])

  async function onLogout() {
    if (loggingOut) return
    setLoggingOut(true)
    let loggedOut = false
    try {
      await logout()
      loggedOut = true
      toast.success('已退出登录')
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        loggedOut = true
      } else {
        toast.error(err instanceof Error ? err.message : '退出登录失败')
      }
    } finally {
      if (loggedOut) {
        setAuthed(false)
        setReady(true)
        navigate('/login', { replace: true })
      }
      setLoggingOut(false)
    }
  }

  if (!ready) {
    return (
      <div className="flex flex-col gap-3 items-center justify-center min-h-screen bg-background text-muted-foreground">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
        <span className="text-sm font-medium">正在验证会话…</span>
      </div>
    )
  }

  if (bootError) {
    return (
      <div className="flex flex-col gap-4 items-center justify-center min-h-screen bg-background px-6 text-center">
        <p className="text-sm font-medium text-foreground">无法连接控制面</p>
        <p className="text-xs text-muted-foreground max-w-md">{bootError}</p>
        <Button onClick={() => void checkSession()} className="cursor-pointer">
          重试
        </Button>
      </div>
    )
  }

  if (!authed) return null

  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col selection:bg-accent selection:text-foreground">
      <a className="sr-only focus:not-sr-only focus:absolute focus:top-4 focus:left-4 bg-primary text-primary-foreground p-3 rounded-md z-50" href="#main-content">
        跳到主要内容
      </a>
      <header className="sticky top-0 z-40 w-full border-b border-border bg-background/80 backdrop-blur-md">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 h-14 flex items-center justify-between gap-4">
          <div className="flex items-center gap-6">
            <Link className="flex items-center gap-2.5 text-sm font-bold tracking-tight text-foreground hover:opacity-90 group" to="/" aria-label="Ladder Airport 节点管理">
              <div className="relative flex items-center justify-center rounded-md overflow-hidden transition-all duration-300 group-hover:scale-105 shadow-[0_0_10px_rgba(6,182,212,0.4)] dark:shadow-[0_0_12px_rgba(168,85,247,0.45)]">
                <img src={theme === 'dark' ? LOGO_DARK_URL : LOGO_LIGHT_URL} alt="Logo" className="w-8 h-8 object-contain" />
              </div>
              <span className="hidden sm:inline">Ladder Airport</span>
            </Link>
            <nav className="flex items-center gap-1 sm:gap-2" aria-label="主导航">
              {NAV.map((item) => (
                <Link
                  key={item.path}
                  to={item.path}
                  className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm font-medium transition-colors ${
                    active === item.path
                      ? 'bg-secondary text-secondary-foreground border border-border/60'
                      : 'border border-transparent text-muted-foreground hover:text-foreground hover:bg-muted/50'
                  }`}
                  aria-current={active === item.path ? 'page' : undefined}
                >
                  {item.icon}
                  <span className="hidden sm:inline">{item.label}</span>
                </Link>
              ))}
            </nav>
          </div>
          
          <TooltipProvider>
            <div className="flex items-center gap-1.5">
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-muted-foreground hover:text-foreground hover:bg-muted/50 cursor-pointer"
                    onClick={toggleTheme}
                    aria-label={theme === 'dark' ? '切换到浅色主题' : '切换到深色主题'}
                  >
                    {theme === 'dark' ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
                  </Button>
                </TooltipTrigger>
                <TooltipContent className="bg-popover border-border text-popover-foreground">
                  {theme === 'dark' ? '浅色模式' : '深色模式'}
                </TooltipContent>
              </Tooltip>

              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-muted-foreground hover:text-foreground hover:bg-muted/50 cursor-pointer"
                    loading={loggingOut}
                    onClick={() => void onLogout()}
                    aria-label="退出登录"
                  >
                    {!loggingOut && <LogOut className="h-4 w-4" />}
                  </Button>
                </TooltipTrigger>
                <TooltipContent className="bg-popover border-border text-popover-foreground">退出登录</TooltipContent>
              </Tooltip>
            </div>
          </TooltipProvider>
        </div>
      </header>

      <main id="main-content" className="flex-1 max-w-7xl w-full mx-auto px-4 sm:px-6 lg:px-8 py-6 focus:outline-none" tabIndex={-1}>
        <Suspense fallback={<PageFallback />}>
          <Outlet />
        </Suspense>
      </main>
    </div>
  )
}

export default function App() {
  return (
    <>
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route element={<AppLayout />}>
            <Route path="/" element={<Fleet />} />
            <Route path="/inbounds" element={<Inbounds />} />
            <Route path="/chains" element={<ProxyChains />} />
            <Route path="/route-plans" element={<RoutePlans />} />
            <Route path="/subscriptions" element={<Subscriptions />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="/pki" element={<PKI />} />
            <Route path="/dns-certificates" element={<DNSCertificates />} />
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Suspense>
      <Toaster />
    </>
  )
}
