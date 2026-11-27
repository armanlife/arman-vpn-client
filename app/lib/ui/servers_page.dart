import 'dart:async';

import 'package:flutter/physics.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/scheduler.dart';

import 'package:material_ui/material_ui.dart';

import '../app_settings.dart';
import '../core/core_api.dart';
import '../core/core_controller.dart';
import '../core/models.dart';
import '../l10n/app_localizations.dart';
import 'format.dart';
import 'browser_signin.dart';
import 'widgets.dart';

/// The server list, one choice in one list: Recommended and Fastest, which
/// the app picks, then the locations with their real delay. A tap on a row
/// chooses it (`proxy.country`: empty for Mozilla's recommended location,
/// `fastest` for the lowest real delay, otherwise a country).
class ServersPage extends StatefulWidget {
  const ServersPage({super.key, required this.core, required this.settings});

  final CoreController core;
  final AppSettings settings;

  @override
  State<ServersPage> createState() => _ServersPageState();
}

/// How old measured delays are before the page measures them again when
/// it opens.
const _pingStale = Duration(minutes: 10);

class _ServersPageState extends State<ServersPage> {
  List<Location>? _locations;
  DateTime? _pingedAt;
  String _country = '';
  bool _testing = false;

  /// The running delay test, which [_stop] cancels; [_runId] tells its
  /// late events from those of a newer one.
  StreamSubscription<PingEvent>? _run;
  int _runId = 0;

  /// The locations the running test measures and has no result for yet.
  Set<String> _pending = {};
  int _done = 0;
  int _total = 0;

  /// The choice being saved ('' recommended, 'fastest', or a code).
  String? _busy;
  String? _summary;

  CoreController get _core => widget.core;

  /// No account yet: the delays cannot be measured (Mozilla's servers
  /// answer only an account's pass), so the page says to add one instead.
  bool get _noAccounts => _core.status?.totalAccounts == 0;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _run?.cancel();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final list = await _core.call((api) => api.locations());
      final config = await _core.settings();
      if (!mounted) return;
      setState(() {
        _locations = list.locations;
        _pingedAt = list.pingedAt;
        if (config['proxy'] case {'country': final String country}) {
          _country = country;
        }
      });
      // Delays measured a while ago, or never: measured again at once (the
      // core does it also while the proxy is off).
      final at = _pingedAt;
      if (!_noAccounts &&
          (at == null || DateTime.now().difference(at) > _pingStale)) {
        unawaited(_test());
      }
    } on CoreApiException catch (e) {
      if (mounted) showError(context, e.message);
    } on CoreUnavailable {
      // The shell shows it.
    }
  }

  Future<void> _choose(String country) async {
    if (_busy != null || _country.toLowerCase() == country.toLowerCase()) {
      return;
    }
    setState(() => _busy = country);
    final ok = await runShowingErrors(
      context,
      () => _core.setLocation(country),
    );
    if (!mounted) return;
    setState(() {
      _busy = null;
      if (ok) _country = country;
    });
  }

  /// [fresh]: the button was pressed, so every location is measured; a
  /// test the page starts by itself keeps slow locations' last figures.
  /// Each location's result shows as it comes.
  Future<void> _test({bool fresh = false}) async {
    final l = AppLocalizations.of(context);
    final codes = [
      for (final loc in _locations ?? const <Location>[]) loc.code,
    ];
    final id = ++_runId;
    await _run?.cancel();
    if (!mounted) return;
    setState(() {
      _testing = true;
      _summary = null;
      _pending = codes.toSet();
      _done = 0;
      _total = 0;
    });
    try {
      final events = await _core.call(
        (api) async => api.pingLocationsLive(codes, fresh: fresh),
      );
      if (!mounted || id != _runId) return;
      _run = events.listen(
        (event) {
          if (!mounted || id != _runId) return;
          setState(() => _apply(l, event));
        },
        onError: (Object error) {
          if (!mounted || id != _runId) return;
          setState(_finish);
          // The core's own words are for the log; the user gets what to do.
          showError(
            context,
            error is CoreUnavailable
                ? describeProblem(l, error)
                : l.delayTestFailed,
          );
        },
        onDone: () {
          if (mounted && id == _runId && _testing) setState(_finish);
        },
      );
    } on CoreUnavailable catch (e) {
      if (!mounted || id != _runId) return;
      setState(_finish);
      showError(context, describeProblem(l, e));
    }
  }

  /// Takes one record of the running test into the list.
  void _apply(AppLocalizations l, PingEvent event) {
    switch (event.type) {
      case 'start':
        // The locations measured now; the others keep their last figure.
        _pending = event.codes.toSet();
        _total = event.codes.length;
      case 'location':
        final loc = event.location;
        if (loc == null) return;
        _replace({loc.code: loc});
        _pending = {..._pending}..remove(loc.code);
        _done++;
      case 'result':
        final result = event.result;
        if (result == null) return;
        _replace({for (final loc in result.locations) loc.code: loc});
        _pingedAt = result.pingedAt ?? DateTime.now();
        final answered = result.locations.where((loc) => loc.answered).length;
        _summary = l.delaySummary(answered, result.locations.length);
        _finish();
      case 'error':
        _finish();
        showError(context, l.delayTestFailed);
    }
  }

  /// Puts the measured locations in place of their rows.
  void _replace(Map<String, Location> tested) {
    _locations = [
      for (final loc in _locations ?? const <Location>[])
        tested[loc.code] ?? loc,
    ];
  }

  /// Ends the running test in the page: no row waits any more.
  void _finish() {
    _testing = false;
    _pending = {};
  }

  /// Stops the running test; the results that came stay.
  void _stop() {
    _runId++;
    unawaited(_run?.cancel());
    _run = null;
    setState(_finish);
  }

  /// Every country, without Mozilla's recommended location (the choice
  /// above), fastest first, then by name in the app's language. While a
  /// test runs, a location waiting for its result keeps its place by its
  /// last figure, and each result moves its row (it slides: [_Slide]).
  List<Location> _countries(AppLocalizations l) {
    final list = [
      for (final loc in _locations ?? const <Location>[])
        if (!isRecommendedPlace(loc.code)) loc,
    ];
    list.sort((a, b) {
      final da = a.answered ? a.pingMs : 1 << 30;
      final db = b.answered ? b.pingMs : 1 << 30;
      if (da != db) return da.compareTo(db);
      return nameSortKey(
        l.localeName,
        locationName(l, a.code, a.name),
      ).compareTo(nameSortKey(l.localeName, locationName(l, b.code, b.name)));
    });
    return list;
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final chosen = _country.toLowerCase();
    final countries = _countries(l);
    final recommended = (_locations ?? const <Location>[])
        .where((loc) => isRecommendedPlace(loc.code))
        .firstOrNull;
    final delayLine =
        _summary ??
        (_pingedAt == null ? null : l.testedAgo(formatAgo(l, _pingedAt!)));
    final noAccounts = _noAccounts;
    return ListView(
      padding: pagePadding(context),
      children: [
        PageTitle(l.navServers),
        if (noAccounts) ...[
          NoAccountsPanel(
            onAdd: () => startAddAccount(context, _core, widget.settings),
          ),
          const SizedBox(height: 12),
        ],
        _SectionHeader(title: l.autoSection),
        _ChoiceCard(
          children: [
            _ChoiceRow(
              leading: const CountryFlag(code: 'REC', width: 36),
              title: l.modeRecommended,
              subtitle: l.modeRecommendedHint,
              delay: recommended == null
                  ? null
                  : _Delay(
                      location: recommended,
                      testing: _pending.contains(recommended.code),
                    ),
              selected: chosen == '',
              busy: _busy == '',
              onTap: () => _choose(''),
            ),
            _ChoiceRow(
              leading: const _IconTile(icon: Icons.bolt_rounded),
              title: l.modeFastest,
              subtitle: l.modeFastestHint,
              selected: chosen == 'fastest',
              busy: _busy == 'fastest',
              onTap: () => _choose('fastest'),
            ),
          ],
        ),
        const SizedBox(height: 20),
        _SectionHeader(
          title: l.locationsSection,
          action: _testing
              ? TextButton.icon(
                  onPressed: _stop,
                  icon: const Icon(Icons.stop_rounded, size: 18),
                  label: Text(l.stopDelayTest),
                )
              : TextButton.icon(
                  onPressed: _locations != null && !noAccounts
                      ? () => _test(fresh: true)
                      : null,
                  icon: const Icon(Icons.speed_rounded, size: 18),
                  label: Text(l.testDelay),
                ),
        ),
        // The progress and the line after it differ in height: the list
        // below moves softly between them.
        AnimatedSize(
          duration: const Duration(milliseconds: 300),
          curve: Curves.easeInOut,
          alignment: AlignmentDirectional.topStart,
          child: _testing
              ? Padding(
                  padding: const EdgeInsetsDirectional.fromSTEB(4, 0, 4, 10),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        _total == 0
                            ? l.testingDelay
                            : l.delayProgress(_done, _total),
                        style: muted,
                      ),
                      const SizedBox(height: 6),
                      LinearProgressIndicator(
                        value: _total == 0 ? null : _done / _total,
                        borderRadius: BorderRadius.circular(999),
                      ),
                    ],
                  ),
                )
              : delayLine != null
              ? Padding(
                  padding: const EdgeInsetsDirectional.fromSTEB(4, 0, 4, 8),
                  child: Text(delayLine, style: muted),
                )
              : const SizedBox(width: double.infinity),
        ),
        if (_locations == null)
          const Padding(
            padding: EdgeInsets.all(24),
            child: Center(child: CircularProgressIndicator()),
          )
        else if (countries.isEmpty)
          Panel(child: Text(l.noServers, style: muted))
        else
          _ChoiceCard(
            children: [
              for (final loc in countries)
                _ChoiceRow(
                  key: ValueKey(loc.code),
                  leading: CountryFlag(code: loc.code, width: 36),
                  title: locationName(l, loc.code, loc.name),
                  subtitle: _subtitle(loc),
                  delay: _Delay(
                    location: loc,
                    testing: _pending.contains(loc.code),
                  ),
                  selected: chosen == loc.code.toLowerCase(),
                  busy: _busy?.toLowerCase() == loc.code.toLowerCase(),
                  onTap: () => _choose(loc.code),
                ),
            ],
          ),
      ],
    );
  }

  /// A location's second line: its city when it says more than the
  /// country.
  String? _subtitle(Location loc) =>
      loc.city.isNotEmpty && loc.city != loc.name ? loc.city : null;
}

class _SectionHeader extends StatelessWidget {
  const _SectionHeader({required this.title, this.action});

  final String title;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsetsDirectional.fromSTEB(4, 8, 0, 6),
      child: Row(
        children: [
          Expanded(
            child: Text(
              title,
              style: theme.textTheme.labelLarge?.copyWith(
                color: theme.colorScheme.primary,
                fontWeight: FontWeight.w600,
              ),
            ),
          ),
          ?action,
        ],
      ),
    );
  }
}

/// Rows of choices in one card, divided by thin lines. Rows with a key
/// slide to a new place when their order changes ([_Slide]).
class _ChoiceCard extends StatelessWidget {
  const _ChoiceCard({required this.children});

  final List<Widget> children;

  @override
  Widget build(BuildContext context) => Card(
    margin: EdgeInsets.zero,
    clipBehavior: Clip.antiAlias,
    child: Column(
      children: [
        for (var i = 0; i < children.length; i++)
          if (children[i].key case final key?)
            _Slide(key: key, divider: i > 0, child: children[i])
          else ...[
            if (i > 0) const Divider(height: 1),
            children[i],
          ],
      ],
    ),
  );
}

/// A row of a list whose order changes: when its place changes, it slides
/// there from where it was on a soft spring instead of jumping, and a row
/// moved again mid-slide goes on from where it is, at the speed it has. It
/// carries the line above it and paints the card's color, so rows passing
/// each other do not show through.
class _Slide extends StatefulWidget {
  const _Slide({super.key, required this.divider, required this.child});

  final bool divider;
  final Widget child;

  @override
  State<_Slide> createState() => _SlideState();
}

class _SlideState extends State<_Slide> with SingleTickerProviderStateMixin {
  /// The spring: critically damped, so it eases in and settles without
  /// passing its place, in about three quarters of a second.
  static final _spring = SpringDescription.withDampingRatio(
    mass: 1,
    stiffness: 70,
  );

  /// The row's distance from its place, in pixels.
  late final _controller = AnimationController.unbounded(vsync: this);

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  void _moved(double shift) {
    if (!mounted) return;
    final velocity = _controller.isAnimating ? _controller.velocity : 0.0;
    _controller.animateWith(SpringSimulation(_spring, shift, 0, velocity));
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return _SlideBox(
      animation: _controller,
      enabled: !MediaQuery.disableAnimationsOf(context),
      onMoved: _moved,
      child: ColoredBox(
        color: theme.cardTheme.color ?? theme.colorScheme.surfaceContainerLow,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (widget.divider) const Divider(height: 1),
            widget.child,
          ],
        ),
      ),
    );
  }
}

class _SlideBox extends SingleChildRenderObjectWidget {
  const _SlideBox({
    required this.animation,
    required this.enabled,
    required this.onMoved,
    super.child,
  });

  final Animation<double> animation;
  final bool enabled;
  final ValueChanged<double> onMoved;

  @override
  _RenderSlide createRenderObject(BuildContext context) =>
      _RenderSlide(animation, enabled, onMoved);

  @override
  void updateRenderObject(BuildContext context, _RenderSlide renderObject) {
    renderObject
      ..animation = animation
      ..enabled = enabled
      ..onMoved = onMoved;
  }
}

/// Paints its row shifted from its laid-out place while it slides. The
/// move is found where the row is painted, so the frame that moves it
/// already shows it where it was, and the slide starts from there; a row
/// moved again mid-slide goes on from where it is seen.
class _RenderSlide extends RenderProxyBox {
  _RenderSlide(this._animation, this.enabled, this.onMoved);

  /// The distance from its place while it slides.
  Animation<double> _animation;
  bool enabled;

  /// Starts a slide from the given distance.
  ValueChanged<double> onMoved;

  set animation(Animation<double> value) {
    if (identical(value, _animation)) return;
    if (attached) {
      _animation.removeListener(markNeedsPaint);
      value.addListener(markNeedsPaint);
    }
    _animation = value;
  }

  /// The place in the list at the last paint.
  double? _lastY;

  /// Moved in this frame: painted [_start] away, the slide starts after it.
  bool _waiting = false;
  double _start = 0;

  double get _shift => _waiting ? _start : _animation.value;

  @override
  void attach(PipelineOwner owner) {
    super.attach(owner);
    _animation.addListener(markNeedsPaint);
  }

  @override
  void detach() {
    _animation.removeListener(markNeedsPaint);
    super.detach();
  }

  /// The move is found where the row is painted, so the frame that moves
  /// it still shows it where it was.
  @override
  void paint(PaintingContext context, Offset offset) {
    if (parentData case final BoxParentData data) {
      final y = data.offset.dy;
      final last = _lastY;
      _lastY = y;
      if (enabled && last != null && last != y) {
        _start = last + _shift - y;
        _waiting = true;
        SchedulerBinding.instance.addPostFrameCallback((_) {
          _waiting = false;
          onMoved(_start);
        });
      }
    }
    super.paint(context, offset.translate(0, _shift));
  }

  @override
  bool hitTestChildren(BoxHitTestResult result, {required Offset position}) =>
      result.addWithPaintOffset(
        offset: Offset(0, _shift),
        position: position,
        hitTest: (result, position) =>
            super.hitTestChildren(result, position: position),
      );

  @override
  void applyPaintTransform(RenderBox child, Matrix4 transform) {
    transform.translateByDouble(0, _shift, 0, 1);
  }
}

/// An icon in the size of a flag, for a choice that is not a country.
class _IconTile extends StatelessWidget {
  const _IconTile({required this.icon});

  final IconData icon;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      width: 36,
      height: 27,
      decoration: BoxDecoration(
        color: scheme.tertiaryContainer,
        borderRadius: BorderRadius.circular(4),
      ),
      child: Icon(icon, size: 20, color: scheme.onTertiaryContainer),
    );
  }
}

/// One choice: what it is, its delay, and a radio mark; a tap chooses it.
class _ChoiceRow extends StatelessWidget {
  const _ChoiceRow({
    super.key,
    required this.leading,
    required this.title,
    required this.selected,
    required this.busy,
    required this.onTap,
    this.subtitle,
    this.delay,
  });

  final Widget leading;
  final String title;
  final String? subtitle;
  final Widget? delay;
  final bool selected;
  final bool busy;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    return Material(
      color: selected
          ? scheme.primary.withValues(alpha: 0.08)
          : Colors.transparent,
      child: InkWell(
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsetsDirectional.fromSTEB(14, 12, 12, 12),
          child: Row(
            children: [
              leading,
              const SizedBox(width: 16),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      title,
                      style: theme.textTheme.titleSmall?.copyWith(
                        fontWeight: selected
                            ? FontWeight.w700
                            : FontWeight.w500,
                      ),
                      overflow: TextOverflow.ellipsis,
                    ),
                    if (subtitle != null) ...[
                      const SizedBox(height: 2),
                      Text(
                        subtitle!,
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: scheme.onSurfaceVariant,
                        ),
                        maxLines: 2,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ],
                  ],
                ),
              ),
              if (delay != null) ...[const SizedBox(width: 10), delay!],
              const SizedBox(width: 6),
              SizedBox.square(
                dimension: 24,
                child: busy
                    ? const Padding(
                        padding: EdgeInsets.all(3),
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : Icon(
                        selected
                            ? Icons.radio_button_checked_rounded
                            : Icons.radio_button_unchecked_rounded,
                        color: selected ? scheme.primary : scheme.outline,
                      ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// A location's real delay as a small badge in its color. While the
/// location is measured, a small spinner, with its last figure faded beside
/// it when there is one. A location that did not answer is grey, not red:
/// it says nothing is wrong with the app.
class _Delay extends StatelessWidget {
  const _Delay({required this.location, required this.testing});

  final Location location;
  final bool testing;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final muted = theme.colorScheme.onSurfaceVariant;
    final loc = location;
    final (text, color) = loc.answered
        ? (l.milliseconds(loc.pingMs), delayColor(loc.pingMs))
        : loc.pingError.isNotEmpty
        ? (l.noAnswer, muted)
        : ('—', muted);
    final label = Text(
      text,
      style: theme.textTheme.labelMedium?.copyWith(
        color: color,
        fontWeight: FontWeight.w700,
      ),
    );
    if (testing) {
      return Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
        decoration: BoxDecoration(
          color: muted.withValues(alpha: 0.10),
          borderRadius: BorderRadius.circular(999),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            SizedBox.square(
              dimension: 12,
              child: CircularProgressIndicator(strokeWidth: 1.5, color: muted),
            ),
            if (loc.answered) ...[
              const SizedBox(width: 6),
              Opacity(opacity: 0.45, child: label),
            ],
          ],
        ),
      );
    }
    // A figure from an earlier test reads as one: faded, and saying so.
    final chip = Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.12),
        borderRadius: BorderRadius.circular(999),
      ),
      child: label,
    );
    if (!loc.cached) return chip;
    return Tooltip(
      message: l.delayFromLastTest,
      child: Opacity(opacity: 0.5, child: chip),
    );
  }
}
