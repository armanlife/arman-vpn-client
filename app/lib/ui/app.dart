import 'dart:io';

import 'package:material_ui/material_ui.dart';

import '../app_settings.dart';
import '../core/android_core.dart';
import '../core/core_api.dart';
import '../core/core_controller.dart';
import '../core/models.dart';
import '../l10n/app_localizations.dart';
import 'about_page.dart';
import 'announcement_panel.dart';
import 'accounts_page.dart';
import 'focus_ring.dart';
import 'format.dart';
import 'logs_page.dart';
import 'mobile_shell.dart';
import 'overview_page.dart';
import 'port_dialog.dart';
import 'servers_page.dart';
import 'settings_page.dart';
import 'welcome_page.dart';
import 'widgets.dart';

ThemeData _theme(Brightness brightness, Locale locale) {
  final scheme = ColorScheme.fromSeed(
    seedColor: const Color(0xFF5B5BD6),
    brightness: brightness,
  );
  final dark = brightness == Brightness.dark;
  // Bundled Inter for English, Vazirmatn for Persian and Arabic, and the
  // platform's typography for the remaining languages.
  final font = languageFont(locale.languageCode);
  final theme = ThemeData(
    colorScheme: scheme,
    fontFamily: font,
    scaffoldBackgroundColor: dark
        ? const Color(0xFF111218)
        : const Color(0xFFF5F5FA),
    cardTheme: CardThemeData(
      elevation: 0,
      color: dark ? const Color(0xFF1A1B23) : Colors.white,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(16),
        side: BorderSide(color: scheme.outlineVariant.withValues(alpha: 0.4)),
      ),
    ),
  );
  return theme.copyWith(
    navigationRailTheme: NavigationRailThemeData(
      backgroundColor: dark ? const Color(0xFF15161D) : const Color(0xFFECECF4),
      // NavigationRail replaces DefaultTextStyle, so retain the resolved
      // platform font instead of letting a null family choose a fallback.
      selectedLabelTextStyle: theme.textTheme.labelLarge!.copyWith(
        fontSize: 15.5,
        fontWeight: FontWeight.w700,
        color: scheme.onSurface,
      ),
      unselectedLabelTextStyle: theme.textTheme.labelLarge!.copyWith(
        fontSize: 15.5,
        fontWeight: FontWeight.w600,
        color: scheme.onSurfaceVariant,
      ),
      selectedIconTheme: IconThemeData(
        size: 24,
        color: scheme.onSecondaryContainer,
      ),
      unselectedIconTheme: IconThemeData(
        size: 24,
        color: scheme.onSurfaceVariant,
      ),
    ),
  );
}

class VpnApp extends StatelessWidget {
  const VpnApp({
    super.key,
    required this.core,
    required this.settings,
    this.navigatorKey,
  });

  final CoreController core;
  final AppSettings settings;
  final GlobalKey<NavigatorState>? navigatorKey;

  static const localizationsDelegates = [
    AppLocalizations.delegate,
    ...GlobalMaterialLocalizations.delegates,
  ];

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: settings,
    builder: (context, _) => MaterialApp(
      navigatorKey: navigatorKey,
      onGenerateTitle: (context) => AppLocalizations.of(context).appTitle,
      debugShowCheckedModeBanner: false,
      locale: settings.locale,
      supportedLocales: AppLocalizations.supportedLocales,
      localeListResolutionCallback: (locales, _) =>
          AppSettings.localeOf(AppSettings.languageForList(locales)),
      localizationsDelegates: localizationsDelegates,
      // The theme depends on the resolved language for its font.
      builder: (context, child) {
        final locale = Localizations.localeOf(context);
        final dark = switch (settings.themeMode) {
          ThemeMode.dark => true,
          ThemeMode.light => false,
          ThemeMode.system =>
            MediaQuery.platformBrightnessOf(context) == Brightness.dark,
        };
        return Theme(
          data: _theme(dark ? Brightness.dark : Brightness.light, locale),
          // A clear ring around what a remote or the keyboard chose.
          child: FocusRing(
            atStart: Platform.isAndroid && AndroidCore.isTv,
            child: child!,
          ),
        );
      },
      home: Shell(core: core, settings: settings),
    ),
  );
}

class Shell extends StatefulWidget {
  const Shell({super.key, required this.core, required this.settings});

  final CoreController core;
  final AppSettings settings;

  @override
  State<Shell> createState() => _ShellState();
}

enum _Page { overview, servers, accounts, settings, logs, about }

class _ShellState extends State<Shell> {
  _Page _page = _Page.overview;

  /// The settings section the home page opened (Connection), until another
  /// page is chosen.
  SettingsSection? _settingsOpening;

  @override
  void initState() {
    super.initState();
    AndroidCore.homeRequests.addListener(_openHome);
  }

  void _openHome() {
    Navigator.of(context).popUntil((route) => route.isFirst);
    setState(() {
      _page = _Page.overview;
      _settingsOpening = null;
    });
  }

  @override
  void dispose() {
    AndroidCore.homeRequests.removeListener(_openHome);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: widget.core,
    builder: (context, _) {
      final l = AppLocalizations.of(context);
      final core = widget.core;
      final status = core.status;
      final needsAttention = (core.accounts?.accounts ?? const [])
          .where((a) => a.enabled && a.needsSignIn)
          .length;
      if (widget.settings.firstRun && status != null) {
        return WelcomePage(core: core, settings: widget.settings);
      }
      final pages = [
        _Page.overview,
        _Page.servers,
        _Page.accounts,
        _Page.settings,
        _Page.logs,
        _Page.about,
      ];
      final page = pages.contains(_page) ? _page : _Page.overview;
      Widget accountsIcon(IconData icon) => Badge(
        isLabelVisible: needsAttention > 0,
        label: Text('$needsAttention'),
        child: Icon(icon),
      );
      // Each page's icons and name, for the side menu and the bottom bar.
      final items = [
        for (final page in pages)
          switch (page) {
            _Page.overview => (
              const Icon(Icons.dashboard_outlined),
              const Icon(Icons.dashboard_rounded),
              l.navOverview,
            ),
            _Page.servers => (
              const Icon(Icons.public_outlined),
              const Icon(Icons.public_rounded),
              l.navServers,
            ),
            _Page.accounts => (
              accountsIcon(Icons.people_outline_rounded),
              accountsIcon(Icons.people_rounded),
              l.navAccounts,
            ),
            _Page.settings => (
              const Icon(Icons.settings_outlined),
              const Icon(Icons.settings_rounded),
              l.navSettings,
            ),
            _Page.logs => (
              const Icon(Icons.receipt_long_outlined),
              const Icon(Icons.receipt_long_rounded),
              l.navLogs,
            ),
            _Page.about => (
              const Icon(Icons.info_outline_rounded),
              const Icon(Icons.info_rounded),
              l.navAbout,
            ),
          },
      ];
      final content = Column(
        children: [
          if (core.unavailable != null && status != null) const _LostBanner(),
          Expanded(
            child: status == null
                ? _NoCore(core: core)
                : switch (page) {
                    // Its cards open the pages beside it.
                    _Page.overview => OverviewPage(
                      core: core,
                      settings: widget.settings,
                      onOpenServers: () =>
                          setState(() => _page = _Page.servers),
                      onOpenAccounts: () =>
                          setState(() => _page = _Page.accounts),
                      onOpenConnection: () => setState(() {
                        _page = _Page.settings;
                        _settingsOpening = SettingsSection.connection;
                      }),
                      onOpenUpstreamProxy: () => setState(() {
                        _page = _Page.settings;
                        _settingsOpening = SettingsSection.upstreamProxy;
                      }),
                    ),
                    _Page.servers => ServersPage(
                      core: core,
                      settings: widget.settings,
                    ),
                    _Page.accounts => AccountsPage(
                      core: core,
                      settings: widget.settings,
                    ),
                    _Page.settings => SettingsPage(
                      key: ValueKey(_settingsOpening),
                      core: core,
                      settings: widget.settings,
                      opening: _settingsOpening,
                      onOpenLogs: () => setState(() => _page = _Page.logs),
                    ),
                    _Page.logs => LogsPage(
                      core: core,
                      settings: widget.settings,
                    ),
                    _Page.about => AboutPage(core: core),
                  },
          ),
        ],
      );
      void select(int i) => setState(() {
        _page = pages[i];
        _settingsOpening = null;
      });
      // Android (a phone, or a tablet in either orientation) and a narrow
      // window: the phone layout, Home and Settings. The computer layout
      // offers computer-only choices, such as Windows' system proxy.
      if (Platform.isAndroid || MediaQuery.sizeOf(context).width < 640) {
        return MobileShell(
          core: core,
          settings: widget.settings,
          banner: core.unavailable != null && status != null
              ? const _LostBanner()
              : null,
          noCore: status == null ? _NoCore(core: core) : null,
        );
      }
      return Scaffold(
        body: AnnouncementOverlay(
          settings: widget.settings,
          onOpenUpdates: () => setState(() => _page = _Page.about),
          child: Row(
            children: [
              NavigationRail(
                selectedIndex: pages.indexOf(page),
                onDestinationSelected: select,
                // Icons with their names beside them.
                extended: true,
                minExtendedWidth: 190,
                leading: Padding(
                  padding: const EdgeInsets.fromLTRB(16, 16, 16, 20),
                  child: Row(
                    children: [
                      const AppLogo(size: 34),
                      const SizedBox(width: 10),
                      Text(
                        l.appTitle,
                        style: Theme.of(context).textTheme.titleMedium
                            ?.copyWith(fontWeight: FontWeight.w600),
                      ),
                    ],
                  ),
                ),
                destinations: [
                  for (final (icon, selected, label) in items)
                    NavigationRailDestination(
                      icon: icon,
                      selectedIcon: selected,
                      label: Text(label),
                    ),
                ],
                // The traffic at the bottom of the menu; in a short window the
                // pages above it scroll rather than run under it.
                trailing: _TrafficPanel(traffic: status?.traffic),
                trailingAtBottom: true,
                scrollable: true,
              ),
              // A maximized window: the pages stay as wide as reads well; the
              // home page, centered by itself, keeps its colors to the edges.
              Expanded(
                child: page == _Page.overview || status == null
                    ? content
                    : Align(
                        alignment: Alignment.topCenter,
                        child: ConstrainedBox(
                          constraints: const BoxConstraints(maxWidth: 1000),
                          child: content,
                        ),
                      ),
              ),
            ],
          ),
        ),
      );
    },
  );
}

/// The side menu's traffic, in one card: the speed now, over a line of
/// the last minute's, then the total since the core started; down in
/// [downloadColor], up in [uploadColor], each named, with a dot of its color.
class _TrafficPanel extends StatefulWidget {
  const _TrafficPanel({required this.traffic});

  final Traffic? traffic;

  @override
  State<_TrafficPanel> createState() => _TrafficPanelState();
}

class _TrafficPanelState extends State<_TrafficPanel> {
  /// The speeds kept for the line, oldest first: about a minute at the
  /// status's pace.
  static const samples = 30;

  final _down = <int>[];
  final _up = <int>[];

  @override
  void initState() {
    super.initState();
    _note(widget.traffic);
  }

  @override
  void didUpdateWidget(_TrafficPanel old) {
    super.didUpdateWidget(old);
    // A new status brings new figures; a rebuild for anything else does not.
    if (!identical(old.traffic, widget.traffic)) _note(widget.traffic);
  }

  void _note(Traffic? t) {
    if (t == null) return;
    _down.add(t.downRate);
    _up.add(t.upRate);
    if (_down.length > samples) {
      _down.removeAt(0);
      _up.removeAt(0);
    }
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final t = widget.traffic;
    const down = downloadColor;
    final title = theme.textTheme.labelSmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
      fontWeight: FontWeight.w600,
    );
    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 0, 12, 16),
      child: Container(
        width: 166,
        padding: const EdgeInsets.fromLTRB(12, 10, 12, 12),
        decoration: BoxDecoration(
          color: theme.colorScheme.surfaceContainerHighest.withValues(
            alpha: 0.5,
          ),
          borderRadius: BorderRadius.circular(14),
        ),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(l.liveTraffic, style: title),
            const SizedBox(height: 8),
            SizedBox(
              height: 32,
              width: double.infinity,
              child: CustomPaint(
                painter: _SpeedLine(
                  down: List.of(_down),
                  up: List.of(_up),
                  samples: samples,
                  downColor: down,
                  upColor: uploadColor,
                  baseline: theme.colorScheme.outlineVariant.withValues(
                    alpha: 0.6,
                  ),
                ),
              ),
            ),
            const SizedBox(height: 8),
            _TrafficFigure(
              label: l.usageDown,
              color: down,
              text: formatRate(t?.downRate ?? 0),
            ),
            const SizedBox(height: 4),
            _TrafficFigure(
              label: l.usageUp,
              color: uploadColor,
              text: formatRate(t?.upRate ?? 0),
            ),
            const Padding(
              padding: EdgeInsets.symmetric(vertical: 10),
              child: Divider(height: 1),
            ),
            Text(l.totalTraffic, style: title),
            const SizedBox(height: 6),
            _TrafficFigure(
              label: l.usageDown,
              color: down,
              text: formatBytes(t?.down ?? 0),
            ),
            const SizedBox(height: 4),
            _TrafficFigure(
              label: l.usageUp,
              color: uploadColor,
              text: formatBytes(t?.up ?? 0),
            ),
          ],
        ),
      ),
    );
  }
}

/// A figure under its name, download or upload, with a small dot of the
/// color of its line in the chart.
class _TrafficFigure extends StatelessWidget {
  const _TrafficFigure({
    required this.label,
    required this.color,
    required this.text,
  });

  final String label;
  final Color color;
  final String text;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Row(
      children: [
        Container(
          width: 6,
          height: 6,
          decoration: BoxDecoration(color: color, shape: BoxShape.circle),
        ),
        const SizedBox(width: 6),
        // The figure always shows whole; a long name gives way.
        Expanded(
          child: Text(
            label,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ),
        const SizedBox(width: 6),
        Text(
          text,
          style: theme.textTheme.bodySmall?.copyWith(
            fontWeight: FontWeight.w600,
            fontFeatures: const [FontFeature.tabularFigures()],
          ),
        ),
      ],
    );
  }
}

/// The last minute's speeds: download as a soft filled curve, upload as a
/// thin line over it, newest on the right, both on one scale.
class _SpeedLine extends CustomPainter {
  _SpeedLine({
    required this.down,
    required this.up,
    required this.samples,
    required this.downColor,
    required this.upColor,
    required this.baseline,
  });

  final List<int> down;
  final List<int> up;
  final int samples;
  final Color downColor;
  final Color upColor;
  final Color baseline;

  @override
  void paint(Canvas canvas, Size size) {
    final floor = size.height - 0.5;
    canvas.drawLine(
      Offset(0, floor),
      Offset(size.width, floor),
      Paint()
        ..color = baseline
        ..strokeWidth = 1,
    );
    var top = 0;
    for (final v in [...down, ...up]) {
      if (v > top) top = v;
    }
    if (down.length < 2 || top == 0) return;
    final step = size.width / (samples - 1);
    List<Offset> points(List<int> values) => [
      for (var i = 0; i < values.length; i++)
        Offset(
          size.width - step * (values.length - 1 - i),
          floor - (size.height - 3) * values[i] / top,
        ),
    ];
    // Through the midpoints, so the line bends rather than breaks.
    Path curve(List<Offset> pts) {
      final path = Path()..moveTo(pts.first.dx, pts.first.dy);
      for (var i = 1; i < pts.length; i++) {
        final mid = (pts[i - 1] + pts[i]) / 2;
        path.quadraticBezierTo(pts[i - 1].dx, pts[i - 1].dy, mid.dx, mid.dy);
      }
      return path..lineTo(pts.last.dx, pts.last.dy);
    }

    final downPts = points(down);
    final downLine = curve(downPts);
    final area = Path.from(downLine)
      ..lineTo(downPts.last.dx, floor)
      ..lineTo(downPts.first.dx, floor)
      ..close();
    canvas.drawPath(
      area,
      Paint()
        ..shader = LinearGradient(
          begin: Alignment.topCenter,
          end: Alignment.bottomCenter,
          colors: [
            downColor.withValues(alpha: 0.25),
            downColor.withValues(alpha: 0),
          ],
        ).createShader(Offset.zero & size),
    );
    final stroke = Paint()
      ..style = PaintingStyle.stroke
      ..strokeJoin = StrokeJoin.round
      ..strokeCap = StrokeCap.round;
    canvas.drawPath(
      downLine,
      stroke
        ..color = downColor
        ..strokeWidth = 1.6,
    );
    canvas.drawPath(
      curve(points(up)),
      stroke
        ..color = upColor.withValues(alpha: 0.85)
        ..strokeWidth = 1.3,
    );
  }

  @override
  bool shouldRepaint(_SpeedLine old) =>
      old.downColor != downColor ||
      old.upColor != upColor ||
      old.baseline != baseline ||
      !_same(old.down, down) ||
      !_same(old.up, up);

  static bool _same(List<int> a, List<int> b) {
    if (a.length != b.length) return false;
    for (var i = 0; i < a.length; i++) {
      if (a[i] != b[i]) return false;
    }
    return true;
  }
}

class _LostBanner extends StatelessWidget {
  const _LostBanner();

  @override
  Widget build(BuildContext context) => Container(
    width: double.infinity,
    color: StatusColors.bad.withValues(alpha: 0.14),
    padding: const EdgeInsets.symmetric(horizontal: 28, vertical: 10),
    child: Row(
      children: [
        const Icon(Icons.link_off_rounded, size: 18, color: StatusColors.bad),
        const SizedBox(width: 10),
        Expanded(child: Text(AppLocalizations.of(context).coreLost)),
      ],
    ),
  );
}

class _NoCore extends StatelessWidget {
  const _NoCore({required this.core});

  final CoreController core;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final problem = core.unavailable;
    final portTaken =
        problem != null &&
        (problem.problem == CoreProblem.portInUse ||
            problem.problem == CoreProblem.refused);
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 460),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (problem == null || problem.problem == CoreProblem.starting) ...[
              const CircularProgressIndicator(),
              const SizedBox(height: 16),
              Text(l.startingCore, style: theme.textTheme.titleMedium),
            ] else ...[
              Icon(
                Icons.power_off_rounded,
                size: 48,
                color: theme.colorScheme.onSurfaceVariant,
              ),
              const SizedBox(height: 16),
              Text(l.cantReachCore, style: theme.textTheme.titleLarge),
              const SizedBox(height: 8),
              Text(
                describeProblem(l, problem),
                textAlign: TextAlign.center,
                style: theme.textTheme.bodyMedium?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
              const SizedBox(height: 20),
              Wrap(
                spacing: 12,
                alignment: WrapAlignment.center,
                children: [
                  if (portTaken && core.moveStoppedCore != null)
                    FilledButton.icon(
                      onPressed: () async {
                        final port = await showPortDialog(
                          context,
                          taken: problem.detail,
                        );
                        if (port != null && context.mounted) {
                          await runShowingErrors(
                            context,
                            () => core.setAppPort(port),
                          );
                        }
                      },
                      icon: const Icon(Icons.settings_ethernet_rounded),
                      label: Text(l.changePort),
                    ),
                  FilledButton.tonalIcon(
                    onPressed: core.retry,
                    icon: const Icon(Icons.refresh_rounded),
                    label: Text(l.retry),
                  ),
                ],
              ),
            ],
          ],
        ),
      ),
    );
  }
}
